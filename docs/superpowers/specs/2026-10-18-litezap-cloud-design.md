# LiteZap Cloud — Design (Peça #3)

Data: 2026-10-18
Status: rascunho para revisão (nenhuma decisão de "Decisões em aberto" está fechada)
Base: motor até o marco 4 + app local v1 (branch `integration/preview`). Specs relacionados: `2026-10-17-litezap-engine-design.md` (decisões 7–9, seções 4.6 e 9) e `2026-10-17-zapi-parity.md` (marco M11, branch `docs/zapi-parity`).

Convenções deste documento:

- **[fonte]** links para a página pública consultada em 2026-10-17. Preços mudam: revalidar antes de publicar a página de preços.
- **(estimativa)** número calculado por nós, não medido.
- **(incerto)** informação que não conseguimos confirmar numa fonte primária.

## 1. Contexto, objetivos e não objetivos

O LiteZap segue o modelo Supabase (decisão 7 do motor): o mesmo binário open source (Apache-2.0) roda no PC (app local), no servidor do próprio usuário (self-host) e na nuvem gerenciada. O que é fechado é só o **control plane** da nuvem: contas, provisionamento, cobrança, pool de IPs de saída e operação.

A nuvem deve parecer, para quem já usou, com o **Supabase Cloud** e o **ClickHouse Cloud**: organização → projetos, assistente de criação com região e plano, projeto pronto em menos de um minuto, painel por projeto com logs, métricas e uso, planos com cotas e componentes por uso, teto de gastos, API de gerenciamento e CLI.

### 1.1 Objetivos

1. **Projeto pré-instalado por cliente**: cada projeto é um app Fly.io isolado com uma Machine rodando o binário `litezap` sem modificações e um volume próprio. O cliente recebe uma URL (`https://<ref>.litezap.cloud`), tokens de instância e o painel.
2. **Estabilidade equivalente à Z-API**: sockets do WhatsApp sempre conectados, reconexão automática, upgrades sem perder sessão.
3. **IP de saída brasileiro e exclusivo por instância**, nunca compartilhado entre clientes, fixo durante a vida da instância (decisão 8 do motor).
4. **Cobrança em reais e em dólar**: Pix, boleto e cartão nacional via Asaas; cartão internacional via Stripe. Preço por instância conectada por mês, com componentes por uso.
5. **API de gerenciamento + CLI** para criar projetos e instâncias por programa, que é o que parceiros e integradores (o "Partner API" da Z-API, PAR-1…6 do doc de paridade) precisam.
6. **Dados no Brasil** (região `gru`, São Paulo) e fluxo de exportação/exclusão alinhado à LGPD.
7. **Operação enxuta**: um time pequeno opera a nuvem; tudo que é repetitivo é automatizado no control plane.

### 1.2 Não objetivos (por enquanto)

- Multi-região ativa ou failover entre regiões. Só `gru` no início.
- Escalar um projeto horizontalmente (várias Machines servindo o mesmo projeto). Um projeto = um supervisor; para crescer, aumenta-se o tamanho da Machine ou cria-se outro projeto.
- `ContainerSpawner` (uma Machine por worker). O `ProcessSpawner` atual já isola workers em processos dentro da Machine; revisitar só se o tamanho máximo de Machine virar limite (seção 11, decisão D2).
- Postgres como store do motor. Cada projeto continua com SQLite no seu volume; o Postgres é só do control plane.
- Registro como aparelho principal, chamadas, listas de transmissão (fora do motor).
- Revenda white-label completa (marca do parceiro no painel). Fica para depois de C3.
- Resolver emissão de nota fiscal (só mapeamos o problema, seção 6.6).

## 2. Modelo de produto

### 2.1 Hierarquia

```
Conta (pessoa, login)
 └─ Organização (cobrança, membros, plano)            ≈ Supabase org / ClickHouse org
     └─ Projeto (ref, região, tamanho, URL, admin token) ≈ Supabase project / ClickHouse service
         └─ Instância (um número de WhatsApp, token lzi_, IP de saída)
```

- **Organização**: dona da assinatura, do método de pagamento, do teto de gastos e dos membros. Um plano por organização (como Supabase e ClickHouse). Uma pessoa pode estar em várias organizações.
- **Projeto**: um ambiente LiteZap isolado = um app Fly com uma Machine e um volume. Tem `ref` curto e aleatório (`lz-k3f9q2`), nome, região, tamanho de compute, versão do motor e a URL `https://<ref>.litezap.cloud`. Casos de uso de vários projetos: produção e homologação, um projeto por cliente final de um parceiro, separar números de áreas diferentes.
- **Instância**: o recurso do motor (um número). É o que se cobra. O control plane conhece as instâncias por meio da API admin do projeto e guarda espelho mínimo (id, nome, estado, IP atribuído, datas) para cobrança e operação. Mensagens e sessões **nunca** saem do volume do projeto.

### 2.2 Membros e papéis

Inspirado nos papéis do Supabase (Owner, Administrator, Developer, Read-only) e do ClickHouse (Admin, Developer, Billing, por serviço) [fontes na seção 13]:

| Papel | Pode |
|---|---|
| Owner | tudo, inclusive excluir a organização e transferir propriedade |
| Admin | membros, cobrança, criar/excluir projetos, ver o admin token |
| Developer | criar e operar instâncias, webhooks e chaves; ver logs; não vê cobrança nem admin token |
| Billing | faturas, métodos de pagamento, teto de gastos; nada técnico |
| Read-only | ver projetos, estado, logs e uso |

Papéis por projeto (um membro só em um projeto) ficam para C3 (ClickHouse permite papel por serviço; Supabase só nos planos Team/Enterprise).

### 2.3 Chaves e tokens

Três níveis, para não confundir o que é do control plane com o que é do motor:

| Credencial | Onde vale | Quem cria | Uso |
|---|---|---|---|
| **Chave de gerenciamento** (`lzm_…`) | API do control plane (`api.litezap.cloud`) | membro Admin/Developer, por organização, com escopo opcional por projeto | criar projetos, instâncias, ler uso; CLI; parceiros |
| **Token admin do projeto** | API do motor do projeto | o control plane, no provisionamento | o painel e o control plane falam com o motor; entregue ao cliente só conforme a decisão D5 |
| **Token de instância** (`lzi_…`) | API do motor, só aquela instância | o motor (`POST /instances`) | o cliente integra com a sua aplicação, MCP, n8n etc. |

Sobre o token admin: hoje ele pode tudo, inclusive `settings.proxy_url` e `unsafe_pacing`. Na nuvem esses dois campos são do operador (o IP de saída é do nosso pool e o ritmo protege o pool). Por isso o motor ganha um **token de operador** e uma lista de **settings travadas** (seção 9, item E4). Com isso o cliente pode receber um token admin "de cliente" sem conseguir trocar o proxy nem desligar os pisos de ritmo.

### 2.4 Criação de projeto

Assistente em três passos, como "New project" do Supabase e "Create service" do ClickHouse:

1. Organização e nome do projeto.
2. Região (`São Paulo (gru)`; outras aparecem desabilitadas com "em breve") e tamanho de compute (padrão pelo plano, seção 2.6).
3. Plano (se a organização ainda não tem) e pagamento; no período de teste, só confirmação de e-mail e telefone (seção 7.3).

Provisionamento alvo: **p95 < 60 s** do clique até `ready` (criação do app, volume, segredos, Machine e certificado; seção 3.3). O painel mostra uma página de estado do projeto com cada passo (como a tela "Setting up project" do Supabase) e, ao final, o botão "Conectar número" que leva direto ao QR da primeira instância.

### 2.5 Planos

Estrutura espelhada em Supabase (Free / Pro / Team / Enterprise) e ClickHouse (Basic / Scale / Enterprise): um preço base que inclui cotas, componentes por uso acima da cota, teto de gastos opcional. Valores são **propostos** e dependem do modelo de custo (seção 8.4) e da decisão D3.

| | Teste grátis | Pro | Team | Enterprise |
|---|---|---|---|---|
| Para quem | experimentar | empresa pequena, dev, agência | empresas com vários números e times | volume, compliance, BYOC |
| Preço base | R$ 0 por 7 dias | por instância conectada/mês (seção 8.4) | taxa da organização + por instância | contrato |
| Instâncias | 1 | 1 a 50 por projeto | 1 a 200 por projeto | sob contrato |
| Projetos | 1 | até 5 | ilimitados | ilimitados |
| IP de saída | 1 IP residencial BR por instância, do pool de teste (seção 5.6) | 1 IP residencial BR fixo por instância | idem + escolha de UF/DDD | idem + IP móvel, IPs dedicados por contrato |
| Mensagens/mês incluídas | 1.000 | 30.000 por instância | 60.000 por instância | sob contrato |
| Entregas de webhook | incluídas no limite de mensagens ×3 | 100.000 por instância | 250.000 por instância | sob contrato |
| Mídia armazenada | 1 GB | 5 GB por instância | 10 GB por instância | sob contrato |
| Backups | não | diário, 7 dias | diário, 30 dias + exportação agendada | personalizado |
| Logs no painel | 1 dia | 7 dias | 28 dias | 90 dias + exportação |
| Membros | 1 | 5 | ilimitados | ilimitados |
| SSO/SAML, logs de auditoria | não | não | sim | sim |
| SLA | nenhum | 99,5% (crédito) | 99,9% (crédito) | negociado |
| Suporte | comunidade | e-mail (1 dia útil) | e-mail prioritário (4h úteis) + WhatsApp | dedicado, 1h para sev1 |

Regras gerais:

- **Unidade de cobrança**: instância ativa por dia, rateada no mês (seção 6.3).
- **Teto de gastos** (como o "spend cap" do Supabase Pro): ligado por padrão no Pro. Com o teto ligado, componentes por uso param na cota (envios além da cota de mensagens ficam `queued` com motivo `plan_quota` até o próximo ciclo; webhooks continuam). Criar instância acima do contratado é recusado em vez de cobrado. Desligado, o excedente é cobrado.
- **Alertas de cobrança**: e-mail em 50%, 80% e 100% de cada cota e quando a fatura projetada passar de um valor definido pelo cliente.
- **Mensagens** contam envios aceitos pela fila (`message.sent` com `source: api`); recebidas não contam (o cliente não controla o que recebe). Ver D3.

### 2.6 Tamanhos de compute

Equivalente aos "compute add-ons" do Supabase e aos tamanhos de serviço do ClickHouse. O tamanho é da Machine do projeto; cada instância consome memória de um processo worker.

| Tamanho | Machine Fly | Instâncias recomendadas | Custo Fly em `gru` (US$/mês) | Observação |
|---|---|---|---|---|
| Nano | shared-cpu-1x, 512 MB | até 3 | ~5,96 | padrão do teste e do Pro com poucas instâncias |
| Micro | shared-cpu-1x, 1 GB | até 8 | ~10,82 | |
| Small | shared-cpu-2x, 2 GB | até 16 | ~21,63 | |
| Medium | shared-cpu-4x, 4 GB | até 32 | ~46 (estimativa) | |
| Large | shared-cpu-8x, 8 GB | até 64 | ~94 (estimativa) | Team |
| XL | shared-cpu-8x, 16 GB | até 128 | ~165 (estimativa) | Team/Enterprise; `performance-*` se a CPU virar gargalo (performance-2x 4 GB ≈ US$ 107 em `gru`) |

Custos: preço da região `iad` × multiplicador 1,615 de `gru` mostrado no seletor de região da página de preços do Fly (`gru` é a região mais cara das 17 listadas); Medium, Large e XL extrapolados por nós (CPU compartilhada + RAM extra) [fonte: docs.fly.io/about/pricing]. CPU compartilhada basta porque o trabalho dos workers é quase todo espera de rede (a medir). Reservas anuais do Fly dão ~40% de desconto em CPU e RAM e entram quando a base estabilizar.

Premissas (estimativa, **a medir no C1**): supervisor ~60–100 MB; worker conectado ~40–80 MB em regime, com picos no *history sync* do pareamento (o motor limita cada worker a `worker_memory_max_mb`, padrão 512 MB, que na nuvem cai para 256 MB). As faixas acima reservam ~100 MB por instância mais o supervisor. O control plane sobe o tamanho automaticamente (com aviso e preço mostrado antes no plano) quando o número de instâncias passa da faixa; ou o cliente escolhe manualmente. Trocar de tamanho reinicia a Machine (reconexão de ~10–30 s, seção 3.6).

### 2.7 Pausa de projetos ociosos e o teste grátis

O Supabase pausa projetos Free após uma semana de inatividade e o ClickHouse suspende serviços ociosos para não cobrar compute. Para o LiteZap, **pausar quebra o produto**: um número só recebe mensagens se o socket estiver conectado, e um aparelho vinculado parado por muito tempo é deslogado pelo WhatsApp (relatos falam em ~14 dias sem uso do celular principal e ~30 dias de inatividade do aparelho vinculado; (incerto), sem página oficial encontrada [fonte: green-api.com/en/docs/faq/why-does-the-crash-happen]).

Por isso:

- **Projetos pagos nunca pausam** (`auto_stop_machines = "off"`, Machine sempre ligada).
- **Teste grátis**: 7 dias, 1 projeto, 1 instância, Machine sempre ligada durante o teste. Ao fim, sem plano, o projeto entra em **pausa**: a Machine para (sessão fica no volume), e o painel mostra "assine para retomar". Retomar dentro de 14 dias costuma reconectar sem QR (incerto, depende do WhatsApp). Após 30 dias pausado, o projeto é excluído (com aviso em D-7 e D-1 e exportação disponível).
- Não oferecemos plano Free permanente no lançamento: cada projeto custa Machine + IP residencial dedicado mesmo sem uso (seção 8). O caminho grátis permanente é o **self-host** (seção 2.9), como no Supabase.
- Avaliado e descartado para o lançamento: Free permanente com "suspend" da Machine (Fly suspende a VM e retoma em centenas de ms). A VM retoma rápido, mas o socket do WhatsApp cai durante a suspensão e mensagens recebidas nesse intervalo só chegam quando reconectar. Pode servir para um Free de "só envio" no futuro (D4).

### 2.8 BYOC e dedicado (Enterprise)

Inspirado no BYOC do ClickHouse (o data plane roda na conta de nuvem do cliente; o control plane continua com o fornecedor):

- **Dedicado** (C3): projetos do cliente numa organização Fly própria, só dele, com faturamento nosso. Simples porque é o mesmo provisionamento com outro `org_slug`.
- **BYOC** (depois de C3): o cliente cria uma organização Fly (ou, mais tarde, uma conta AWS/GCP com um runtime de containers) e nos dá um token com escopo limitado; o control plane provisiona lá. Os dados (volumes) e o tráfego ficam na conta do cliente; o cliente paga a infraestrutura direto ao provedor e nos paga licença/suporte por instância. Requisito do motor: nenhum (é o mesmo binário). Requisito do control plane: toda chamada ao Fly passa por uma interface `Provider` (seção 3.2) parametrizada por organização/credencial.
- IP de saída no BYOC: o cliente pode usar o nosso pool (proxy pela internet) ou trazer o próprio.

### 2.9 Self-host grátis

O caminho open source continua de primeira classe, como no Supabase: `litezap serve` em qualquer servidor ou `docker run`, com o mesmo painel. A documentação do site (peça #6) compara "LiteZap Cloud × self-host" com honestidade: o que a nuvem adiciona é operação (upgrades, backups, monitoramento), IP residencial gerenciado e suporte. Não existe recurso do motor exclusivo da nuvem; o que é exclusivo é o control plane.

## 3. Arquitetura

```
                         ┌────────────────────── Control plane (fechado) ──────────────────────┐
 navegador ──────────▶   │ app.litezap.cloud (painel: contas, orgs, cobrança + painel do motor)│
 CLI / parceiros ────▶   │ api.litezap.cloud (API de gerenciamento, lzm_)                       │
 Stripe / Asaas ─────▶   │ webhooks de cobrança                                                 │
                         │                                                                      │
                         │  Postgres (Supabase/Neon, sa-east-1) ◀── workers de provisionamento, │
                         │                                          cobrança, uso, IPs, upgrades│
                         └───────────┬──────────────────────────────────────────┬──────────────┘
                                     │ Fly Machines API                         │ API admin do motor
                                     ▼                                          │ (token de operador, HTTPS)
       ┌──────────── Data plane: organização Fly "litezap-prod", região gru ────▼───────────────┐
       │  app lz-k3f9q2 (rede 6PN própria)        app lz-8hd2mm (rede 6PN própria)       ...   │
       │  ┌──────────────────────────────┐        ┌──────────────────────────────┐            │
       │  │ Machine: litezap serve       │        │ Machine: litezap serve       │            │
       │  │  supervisor + N workers      │        │  supervisor + N workers      │            │
       │  │ volume /data (SQLite, sessões│        │ volume /data                 │            │
       │  │  mídia)                      │        │                              │            │
       │  └──────────┬───────────────────┘        └──────────┬───────────────────┘            │
       └─────────────┼───────────────────────────────────────┼─────────────────────────────────┘
                     │ cada worker sai pelo seu proxy         │
                     ▼                                        ▼
            proxy residencial BR (IP A)            proxy residencial BR (IP B) ──▶ WhatsApp
```

### 3.1 Control plane

Código fechado, repositório separado. Componentes:

- **API de gerenciamento** (`api.litezap.cloud/v1`): organizações, membros, projetos, instâncias, chaves, uso, faturas. Instâncias são criadas aqui e o control plane chama `POST /v1/instances` no motor do projeto, atribui o IP e aplica `proxy_url`. Também repassa (com autorização do control plane) as rotas de instância mais comuns para quem quer uma só URL de API — opcional; a URL do projeto continua sendo a principal.
- **Painel do cliente** (`app.litezap.cloud`): login, organizações, projetos, cobrança, uso, logs. Para o que acontece dentro de um projeto (instâncias, QR, conversas, webhooks, eventos), **reaproveita o painel embutido do motor** (`/app/`): o control plane abre `https://<ref>.litezap.cloud/app/` com login de um clique, usando a mesma ideia do código de uso único do app de bandeja, mas emitido pelo control plane (item E7 da seção 9). Assim, toda melhoria do painel open source chega à nuvem sem retrabalho.
- **Postgres gerenciado** (só metadados: contas, organizações, projetos, instâncias espelhadas, IPs, assinaturas, eventos de cobrança, uso agregado, auditoria). Nenhuma mensagem de WhatsApp. Opções na decisão D6.
- **Workers assíncronos**: provisionamento, sincronização de estado e uso, atribuição e saúde de IPs, ondas de upgrade, cobrança e cobrança atrasada (dunning), exclusão. Fila em Postgres (tabela com `SELECT … FOR UPDATE SKIP LOCKED`; River ou similar se em Go). Toda tarefa é idempotente e retomável (cada passo grava o estado).
- **Webhooks de cobrança**: endpoints para Stripe e Asaas (seção 6.5).
- **Hospedagem do control plane**: o próprio Fly (`gru`), em um app separado da organização dos projetos, com 2 Machines para disponibilidade. Linguagem sugerida: Go (mesma stack, reaproveita o cliente da API do motor).

### 3.2 Interface de provedor

Todo acesso à infraestrutura passa por uma interface do control plane, para permitir dedicado/BYOC e, se um dia preciso, outro provedor:

```
Provider.CreateProject(ref, region, size, image, secrets) → ProjectInfra
Provider.UpdateImage(ref, image, strategy) / Resize(ref, size) / Stop / Start / Destroy
Provider.Snapshot(ref) / Restore(ref, snapshotID)
Provider.Status(ref) → {machine_state, checks, volume_usage}
```

Implementação inicial: `FlyProvider` (Machines API `https://api.machines.dev/v1`).

### 3.3 Data plane: um app Fly por projeto

Recomendação (decisão D1): **um app Fly por projeto**, cada um numa **rede 6PN própria**. O Fly recomenda um app por cliente para plataformas assim: segredos ficam isolados por app, logs e escala separados, e a ferramenta deles não é feita para milhares de Machines no mesmo app [fonte: docs.fly.io/machines/guides-examples/one-app-per-user-why]. Um app criado com o campo `network` no `POST /v1/apps` fica numa 6PN isolada, que não fala com as outras sem configuração explícita; a rede não muda depois de criada [fonte: docs.fly.io/networking/custom-private-networks].

Passos do provisionamento (worker `provision_project`, cada passo idempotente):

1. `POST /v1/apps` com `app_name = <ref>`, `org_slug`, `network = net-<ref>`.
2. Segredos do app: `LITEZAP_ADMIN_TOKEN` (cliente), `LITEZAP_OPERATOR_TOKEN` (E4), `LITEZAP_PUBLIC_URL=https://<ref>.litezap.cloud`. Gerados no control plane e guardados cifrados (seção 7.1).
3. Volume `data` em `gru` (tamanho pelo plano, 1–10 GB, com extensão automática).
4. Machine com a imagem `ghcr.io/…/litezap:<versão>` (`litezap serve`), `LITEZAP_LISTEN_ADDR=0.0.0.0:8080`, `LITEZAP_DATA_DIR=/data`, `LITEZAP_WORKER_MEMORY_MAX_MB=256`, serviço HTTP com `auto_stop_machines = "off"`, `min_machines_running = 1`, checks (3.5), `kill_timeout` de 30 s, `restart.policy = always`.
5. IPs de entrada: IPv6 dedicado + IPv4 compartilhado (o IPv4 dedicado de entrada é cobrado; não precisamos dele porque não há TCP cru) e certificado para `<ref>.litezap.cloud` (DNS com CNAME para `<ref>.fly.dev`).
6. Espera `/readyz`, cria a primeira instância se o assistente pediu, marca o projeto `active`.

Alternativa de roteamento avaliada: um app "gateway" com certificado curinga `*.litezap.cloud` repassando com `fly-replay: app=<ref>`. Descartada para o tráfego da API porque o `fly-replay` não repassa requisições acima de 1 MB e exige liberar replays entre redes [fonte: docs.fly.io/networking/dynamic-request-routing]; o motor aceita corpos de envio de mídia de até 140 MB. Pode servir só para o painel.

### 3.4 Sempre ligado

- `auto_stop_machines = "off"` em todo projeto pago (os sockets do WhatsApp precisam ficar abertos; seção 2.7).
- `restart.policy = "always"`: se o processo `serve` morre, o Fly reinicia a Machine; dentro dela, o supervisor já reinicia workers com backoff e circuit breaker (seção 9.2 do motor).
- Uma Machine por projeto, **sem** Machine reserva: como o volume é local ao host, uma segunda Machine não teria os dados. Se o host do Fly falhar, a recuperação é criar Machine nova a partir do snapshot mais recente do volume (RPO até 24 h) ou esperar o host voltar (o caso comum). Isso limita o SLA possível (seção 8.1). Replicação contínua (Litestream para `litezap.db`) entra em C3 para reduzir o RPO do log de mensagens; as sessões do whatsmeow não devem ser restauradas de backups antigos (3.8).

### 3.5 Health checks

- **Check de Machine** (Fly `checks`, HTTP pela rede interna): `GET /healthz` a cada 15 s, timeout 5 s. Hoje o motor exige token admin em `/healthz` e `/readyz` quando o bind não é loopback; o Fly permite cabeçalhos no check, mas um segredo em `fly.toml` é ruim. Mudança E2: `ops_listen_addr` separado (ex.: `[::]:9090`, só na 6PN) com `/healthz`, `/readyz` e `/metrics` sem token.
- **Check de prontidão** (`/readyz`) usado pelo control plane antes de marcar `ready` e depois de cada upgrade. As estratégias `canary` e `bluegreen` do `fly deploy` não funcionam com volumes [fonte: docs.fly.io/reference/configuration]; por isso as ondas da seção 3.6 são orquestradas pelo control plane, Machine a Machine, e não pelo `fly deploy`.
- **Sonda externa** do control plane a cada 60 s: `GET https://<ref>.litezap.cloud/readyz` + `GET /v1/instances` (operador) para estados. Alimenta a página de estado do projeto e os alertas (seção 8.2).
- **Saúde por instância**: `instance.status` chega ao control plane por um webhook global do operador cadastrado no provisionamento (`events: ["instance.status"]`), com o estado e o motivo (`proxy_unreachable`, `logged_out`, `banned`, `temp_banned:`).

### 3.6 Upgrades em ondas e rollback

- A imagem do motor é versionada por tag semver; cada projeto guarda `engine_version` e `release_channel` (`stable`, `canary`).
- **Ondas**: (0) projetos internos e de teste → (1) canário: 5% dos projetos pagos que optaram pelo canal `canary` ou sorteados entre os de menor tamanho → (2) 25% → (3) 100%. Cada onda espera 24 h (canário) ou 2–6 h (demais) e só avança se os indicadores ficarem dentro do limite: taxa de reconexão bem-sucedida em 5 min ≥ 99%, nenhuma alta de `logged_out`/`banned` acima do normal, taxa de crash de worker, erros 5xx da API.
- **Janela**: atualizações não urgentes entre 02:00 e 05:00 BRT. Correções de segurança podem ir fora da janela.
- **Mecânica**: atualizar a Machine (`POST /v1/apps/<ref>/machines/<id>` com a imagem nova). O Fly envia SIGTERM, o motor faz shutdown gracioso (para despachantes, espera envios em voo até 10 s, desconecta workers) e a Machine sobe com a nova imagem; as sessões voltam do volume sem QR. Indisponibilidade esperada por projeto: 10–30 s (estimativa, medir).
- **Antes de cada upgrade**: snapshot do volume (`POST /v1/apps/<ref>/volumes/<id>/snapshots`).
- **Rollback**: reaplicar a imagem anterior. Isso exige **migrações do SQLite compatíveis com uma versão para trás** (regra nova para o motor: migração destrutiva só duas versões depois da que deixou de usar a coluna). Se a migração não for reversível, rollback = restaurar o snapshot pré-upgrade (perde o que chegou depois; último recurso).
- O cliente vê a versão do motor no painel e pode adiar upgrades não urgentes por até 14 dias (Team+), como as janelas de manutenção do ClickHouse.

### 3.7 Rede

- **Entrada**: só HTTPS pelo proxy do Fly em `<ref>.litezap.cloud`. A API, o painel e o MCP HTTP (`/mcp`) ficam nessa URL. `public_url` é configurado para que URLs assinadas de mídia funcionem.
- **Interna**: 6PN própria por projeto; o control plane fala com os projetos pela URL pública com o token de operador (as redes isoladas não alcançam o control plane e vice-versa, de propósito) e, para métricas, pelo endpoint de ops (via Prometheus gerenciado do Fly, seção 3.9).
- **Saída**: tudo que o worker faz com o WhatsApp (websocket e mídia) passa pelo proxy da instância (seção 4.6 do motor). O supervisor sai direto pelo IP do Fly para baixar `media.url` e entregar webhooks (não fala com o WhatsApp). Isso é aceitável: o IP do Fly nunca encosta no WhatsApp.

### 3.8 Backups e snapshots

- **Snapshots de volume do Fly**: diários e automáticos, padrão de 5 dias, retenção configurável de 1 a 60 dias (`snapshot_retention`) e snapshots sob demanda; US$ 0,08/GB-mês armazenado, 10 GB/mês grátis. O Fly avisa que o que foi escrito depois do último snapshot pode se perder se o host falhar [fonte: docs.fly.io/volumes/snapshots]. Retenção por plano: Pro 7 dias, Team 30 dias.
- **Exportação lógica** (E8): endpoint do motor que gera um arquivo com `litezap.db` (backup online do SQLite), mídia e configurações, enviado a um bucket (Tigris, S3 compatível, ou S3 em `sa-east-1`). Team: exportação agendada semanal para um bucket do próprio cliente.
- **Cuidado com sessões**: o *store* do whatsmeow guarda chaves e estados de *ratchet* do Signal. Restaurar uma cópia antiga pode gerar falhas de decifragem ou exigir novo pareamento (incerto quanto à frequência; tratar restauração de sessão como "melhor esforço"). A restauração padrão recupera o log de mensagens, webhooks e configurações; o cliente reescaneia o QR se a sessão não voltar.
- **Equivalente a PITR**: Litestream do `litezap.db` para o bucket (C3, Team+). Não se aplica às sessões, pelo motivo acima.

### 3.9 Logs e métricas

- **Logs**: o motor já usa `slog` com `instance_id` em toda linha. Mudança E9: saída JSON em stdout quando `LITEZAP_LOG_FORMAT=json`. O Fly agrega o stdout de todas as Machines da organização; o **fly-log-shipper** (app oficial que consome o fluxo NATS da organização) envia para um destino (Better Stack, Axiom, Grafana Loki ou ClickHouse) [fonte: github.com/superfly/fly-log-shipper]. O painel do cliente tem um **explorador de logs** filtrado pelo app do projeto (consulta no destino com filtro obrigatório por `app`); retenção por plano.
- **Métricas**: o Fly tem Prometheus gerenciado por organização e coleta métricas expostas pela app declaradas em `[metrics]` no `fly.toml` [fonte: fly.io/docs/monitoring/metrics]. O motor ainda não expõe `/metrics` (planejado na seção 9.4 do motor; item E3). Métricas mínimas: instâncias por estado, reconexões, mensagens enviadas/recebidas, profundidade e latência da fila, entregas de webhook por status, restarts e memória de worker, falhas de proxy.
- **Painel de observabilidade por projeto**: gráficos dessas métricas (consulta PromQL com filtro `app=<ref>`) e a página de estado do projeto. Grafana interno para a operação.
- **Uso para cobrança** não depende de métricas (amostradas, podem perder pontos): vem de contadores persistidos no motor (E11), lidos pelo worker de uso a cada hora.

### 3.10 Segredos

- Gerados no control plane, guardados no Postgres **cifrados por envelope**: uma chave de dados por projeto (AES-256-GCM), cifrada por uma chave mestra que fica fora do banco (KMS da AWS em `sa-east-1` ou, no C1, um segredo do app do control plane no Fly). Rotação da chave mestra sem recifrar os dados (só as chaves de dados).
- Entregues ao data plane como **segredos do app Fly** (viram variáveis de ambiente; nunca aparecem em `fly.toml` nem em logs).
- Credenciais do proxy de cada instância: guardadas cifradas no control plane e aplicadas no motor via `PUT /settings` com o token de operador; o motor já esconde a senha no `GET /settings`.
- Token de operador: um por projeto, nunca exposto ao cliente, rotacionado a cada 90 dias e em toda suspeita (o motor precisa aceitar dois tokens durante a rotação; E4).

## 4. Fluxo da instância na nuvem

1. Cliente cria instância (painel, CLI ou API de gerenciamento). O control plane verifica plano, teto e cota; reserva um IP do pool (seção 5) compatível com a UF escolhida.
2. Chama `POST /v1/instances` no motor (token de operador) → recebe `{instance, token}`; mostra o token ao cliente uma vez (o control plane não guarda o token de instância, só o id).
3. `PUT /v1/instances/{id}/settings {"proxy_url": "socks5://…"}` com o token de operador (campo travado para o cliente). Trocar o proxy reinicia o worker.
4. Cliente pareia (QR ou código) no painel. Ao receber `instance.status: connected`, o control plane marca a instância como **ativa para cobrança** e registra `ip_id ↔ instance_id ↔ phone_hash` (para a regra "um cliente por IP").
5. Exclusão: `DELETE /v1/instances/{id}` no motor; o IP entra em **quarentena** (seção 5.5) antes de voltar ao pool.

## 5. Estratégia de IP de saída

### 5.1 Por que não usar o IP do Fly direto

- **IP de datacenter**: os IPs do Fly pertencem ao ASN do próprio Fly e são facilmente classificados como datacenter. Não há declaração da Meta, mas há relatos consistentes da comunidade de clientes não oficiais: um projeto baseado em Baileys descreve restrição silenciosa de aparelhos vinculados que conectam de IPs de VPS (o envio "funciona" e a mensagem não chega) [fonte: github.com/arslanstack/whatsapp-baileys]; um tópico de suporte da Railway relata conexão recusada a partir de datacenter e funcionando de casa [fonte: station.railway.com/questions/railway-blocks-websockets-9ac08fce]; a Whapi.Cloud, concorrente, afirma que o WhatsApp compara a sessão com a região do número [fonte: support.whapi.cloud/help-desk/blocking/using-residential-proxies-with-whapi.cloud]. Tudo **anedótico**; na discussão do whatsmeow há quem defenda que banimento vem do comportamento, não da biblioteca [fonte: github.com/tulir/whatsmeow/discussions/979].
- **Contaminação**: um IP de datacenter compartilhado entre clientes marca todos quando um deles faz spam (risco listado no spec do motor).
- **Granularidade**: o IP de saída estático do Fly custa US$ 0,005/h (~US$ 3,60/mês por par IPv4+IPv6), pertence a um app e região, atende até 64 Machines e **não dá para escolher qual Machine usa qual IP** quando o app tem vários; IPs por Machine foram descontinuados [fontes: docs.fly.io/about/pricing; docs.fly.io/networking/egress-ips]. Com um app por projeto daria um IP por projeto, nunca um por instância.

Conclusão: o IP de saída estático do Fly fica como **plano de contingência** (projeto inteiro saindo por um IP fixo de datacenter quando o provedor de proxy cai e o cliente prefere isso a ficar desconectado; opt-in, com aviso) e como IP fixo para o **supervisor** quando um cliente precisa liberar nosso IP no firewall do receptor de webhooks (add-on). Nunca como padrão para os sockets do WhatsApp.

### 5.2 Tipos de proxy

| Tipo | Como é | Para o LiteZap |
|---|---|---|
| **Residencial estático ("ISP")** | IP de faixa de provedor de internet residencial, hospedado em datacenter do fornecedor; dedicado, fixo por meses, banda geralmente ilimitada | **Padrão.** Atende "um IP fixo por instância" e "um cliente por IP" |
| Residencial rotativo (por GB) com sessão fixa | IP de aparelhos reais (SDK em apps); sessão "sticky" de até 24 h a 7 dias; o aparelho pode sumir antes | Não: o IP muda pelo menos semanalmente, o que o motor evita (seção 4.6 do motor: trocar de IP com frequência é sinal suspeito). Única opção com segmentação por região/cidade em todo o Brasil |
| Móvel 4G/5G dedicado | modem/SIM de operadora (TIM, Vivo, Claro), IP atrás de CGNAT compartilhado com usuários reais | Melhor reputação, mas o IP muda por desenho e custa 15–50× mais. Add-on premium (Enterprise) |
| Datacenter | IP de hospedagem | Não (5.1) |

Consumo de banda estimado por instância (texto, sempre conectada): **0,5–1,5 GB/mês** mais o *history sync* do pareamento e a mídia (estimativa; o consumo do app de celular em texto é citado em 1–2 MB/h em blogs [fontes: roamless.com/blog/how-much-data-does-whatsapp-use]). Com proxy por GB a US$ 3,5–5/GB, uma instância de texto custaria US$ 2–7/mês e uma com muita mídia US$ 15–50/mês: o estático por IP é mais barato e previsível.

### 5.3 Fornecedores com IP brasileiro (consulta em 2026-10-17, preços em US$)

| Fornecedor | Estático ISP no Brasil | Preço por IP/mês | Localização no Brasil | Banda | API | Observações | Fonte |
|---|---|---|---|---|---|---|---|
| **IPRoyal** | sim, dedicado; ~10 mil IPs BR | US$ 2,70 (30 dias), 2,55 (60), 2,40 (90); um review de mar/2026 cita **US$ 4/IP para o Brasil** (sobretaxa por país? incerto); sem mínimo | API de pedidos só recebe país; esperar São Paulo | ilimitada | REST: cotar, criar, estender, renovação automática; sem endpoint de troca encontrado | AUP proíbe revenda/acesso de terceiros "sem exceção": exige acordo escrito | iproyal.com/static-residential-proxies; docs.iproyal.com/proxies/isp/api/orders; iproyal.com/acceptable-use-policy; proxyway.com/proxy-locations/brazil-proxy |
| **Bright Data** | sim | dedicado: US$ 3,50 (10 IPs) a 2,50 (1.000); compartilhado 1,80–1,30; franquia de 100 GB/IP/mês | anuncia cidade/estado/CEP; Proxyway diz que estado não vale para o Brasil | 100 GB/IP | sim | KYC rigoroso; revenda só com aprovação escrita | brightdata.com/pricing/proxy-network/isp-proxies; brightdata.com/locations/br; brightdata.com/trustcenter/acceptable-use-policy-bright-data |
| **Proxy-Cheap** | sim, "static residential" dedicado | US$ 2,44 (promo) / 2,87 (lista); 2,71 em planos de 1 e 12 meses | **único que mostra estáticos fora de SP** (Rio, BH, Fortaleza, Porto Alegre, Curitiba, Brasília…, poucas centenas cada); dados da página inconsistentes | ilimitada | existe (não verificada) | validar estoque com compra de teste | proxy-cheap.com/pricing; proxy-cheap.com/locations/brazil |
| **Oxylabs** | sim, mas o produto padrão é **compartilhado com até 3 usuários**; dedicado sob consulta | US$ 1,50–1,10 (compartilhado) | cidade | 50 GB/IP e depois limitado | sim | compartilhado viola "um cliente por IP" | oxylabs.io/pricing/isp-proxies |
| **SOAX** | não vende por IP; "ISP" é filtro de tráfego por GB | US$ 5 a 0,35/GB conforme plano; planos a partir de US$ 200/mês | país, região, cidade, ASN — o mais próximo de casar UF/DDD | por GB | sim | só serve para rotativo | soax.com/pricing |
| Móvel BR (Coronium) | 4G dedicado TIM/Vivo | US$ 129/linha/mês; sem estoque BR na consulta | SP, Rio, BH | ilimitada | sim | IP rota por timer/sob demanda | coronium.io/mobile-proxies/brazil |

Descartados: Decodo/Smartproxy, Webshare e Rayobyte (sem ISP no Brasil); NetNut (rede desmantelada por Google e FBI em 2026-07-02, domínio apreendido; a controladora contesta) [fontes: rayobyte.com/products/isp-proxies; webshare.io/static-residential-proxy; bleepingcomputer.com/news/security/netnut-proxy-network-disrupted-2-million-infected-devices-cut-off].

**Recomendação (D7)**: **IPRoyal** no C1 (dedicado, banda ilimitada, SOCKS5, API de pedidos com renovação automática, sem mínimo nem KYC pesado), com **Proxy-Cheap** em avaliação paralela por ter cidades fora de SP, e **Bright Data** como segundo fornecedor no C3 (mais caro, KYC rigoroso, mais maduro). Antes de assinar: acordo escrito de uso "proxy embutido em SaaS gerenciado" (as AUPs proíbem revenda), confirmação do preço real do Brasil e de como trocar um IP, e compra de 3–5 IPs para checar ASN e reputação. Credenciais de proxy **nunca** vão para o cliente (o motor esconde a senha; o campo é travado, E4).

### 5.4 Regras do pool

- **Um cliente por IP, para sempre dentro da quarentena**: o IP é reservado para uma instância; quando a instância é excluída, o IP só volta ao pool depois da quarentena e **nunca** vai para outra organização antes de 30 dias.
- **Fixo por instância**: o control plane nunca troca o IP de uma instância conectada por conta própria, salvo nas regras de saúde (5.5). Isso segue a regra do motor de não trocar IP durante a sessão.
- **UF/DDD**: o control plane tenta casar a UF do IP com o DDD do número (o DDD só é conhecido depois do pareamento; antes, usa a UF informada pelo cliente). Com os fornecedores atuais, quase todo estoque estático é de São Paulo; casar DDD é "melhor esforço" e só vira recurso vendido (Team) se o fornecedor tiver estoque estável fora de SP. Um IP de SP para um número de outro estado é normal no Brasil (o número é nacional; muita gente usa o WhatsApp em outro estado), então o risco de não casar é baixo (incerto).
- **Estoque**: manter 10–20% de IPs livres e testados (estimativa) para criação instantânea; compra automática pela API do fornecedor quando o estoque livre cai abaixo do limite.
- **Renovação**: pedidos de 30 dias com renovação automática; o worker `ip_pool` concilia diariamente (pedido expirado = alerta antes da expiração).

### 5.5 Saúde e quarentena

Sinais por IP (coletados pelo control plane):

- **Conectividade**: sonda a cada 5 min a partir do control plane (`CONNECT` ao `web.whatsapp.com:443` pelo proxy, latência e erro) e o evento `instance.proxy_failed` (E6) / `reason: proxy_unreachable` vindo do motor.
- **Reputação**: listas públicas de bloqueio (consulta diária), ASN e geolocalização (o IP ainda é BR e residencial?).
- **Resultado no WhatsApp**: taxa de `banned`/`temp_banned`/`logged_out` inesperado das instâncias que usaram o IP nos últimos 30 dias.

Ações:

| Situação | Ação |
|---|---|
| proxy fora por > 2 min | alerta; o motor fica em `reconnecting` (nunca cai para conexão direta) |
| proxy fora por > 10 min ou 3 quedas em 1 h | **troca automática** para um IP livre da mesma UF (reinicia o worker; a sessão continua, sem QR), IP antigo em quarentena, aviso ao cliente |
| instância banida | IP em quarentena de 30 dias; se 2 instâncias no mesmo IP foram banidas em 90 dias (mesmo cliente), o IP é descartado |
| taxa de ban do pool sobe acima do normal (ex.: 2× a média de 30 dias) | alerta sev2, congela novas atribuições do lote/sub-rede afetado, investigação |
| IP aparece em lista de bloqueio ou deixa de ser BR | descarte e troca |

Trocar o IP de uma instância conectada é exceção (cada troca é registrada e limitada a 1 por semana por instância, salvo proxy fora do ar).

### 5.6 Como o control plane aplica o proxy

O campo `settings.proxy_url` é só do token admin hoje e passa a ser travado para o cliente na nuvem (E4). O control plane:

1. Reserva o IP no Postgres (`ip_assignments`, com `UNIQUE` em `ip_id` ativo e checagem de organização).
2. Chama `PUT /v1/instances/{id}/settings {"proxy_url":"socks5://user:pass@host:port"}` com o token de operador. O motor reinicia o worker com o proxy por variável de ambiente (seção 4.6 do motor).
3. Verifica pelo estado (`connected` ou `qr_pending` sem `proxy_unreachable`) e por um eco de IP feito pelo control plane pelo mesmo proxy.
4. `default_proxy_url` do projeto fica **vazio**, e o motor nunca cai para conexão direta; uma instância sem proxy atribuído não é iniciada (o control plane cria a instância e aplica o proxy antes do pareamento; para garantir, o motor ganha `require_proxy: true`, parte de E4, que recusa iniciar worker sem proxy).
5. Teste grátis usa um sub-pool de IPs "de teste" (mesmo tipo, separados do pool pago para que abuso de teste não contamine IPs de clientes pagantes).

### 5.7 Custo de IP por instância

| Item | US$/mês | Fonte/nota |
|---|---|---|
| IP estático ISP (IPRoyal, 30 dias) | 2,70–4,00 | 5.3; usar 4,00 no modelo (pior caso para o Brasil) |
| Estoque livre (15%) | 0,60 | estimativa |
| Quarentena (IP pago parado ~30 dias a cada troca; ~1 troca/instância/ano) | 0,35 | estimativa |
| **Total por instância** | **~5,00** | (estimativa) |

## 6. Cobrança

### 6.1 Por que dois processadores

| | Asaas | Stripe |
|---|---|---|
| Para quem | clientes no Brasil (CPF/CNPJ) | clientes fora do Brasil, ou brasileiros que preferem cartão internacional |
| Meios | Pix, boleto, cartão de crédito BR (com parcelamento), débito | cartão (nacional e internacional), carteiras |
| Taxas (2026-10-17) | Pix e boleto R$ 1,99 por cobrança paga (R$ 0,99 nos 3 primeiros meses); cartão à vista 2,99% + R$ 0,49; NFS-e R$ 0,49 por nota; sem mensalidade [fonte: asaas.com/precos-e-taxas] | Stripe Brasil: cartão nacional 3,99% + R$ 0,39; internacional +2%; Billing 0,7% do volume cobrado [fonte: stripe.com/br/pricing]. Pix no Stripe é só por convite (1,19%), boleto R$ 3,45 |
| Moeda | BRL | USD (preço em dólar para clientes de fora) ou BRL |

A tabela de assinaturas do Asaas e o FAQ divergem sobre a taxa de cartão recorrente (1,99% × 2,99%) (incerto). Stripe US (2,9% + US$ 0,30) só se aplicaria com uma entidade nos EUA (decisão D8).

### 6.2 Modelo de dados de cobrança

O **control plane é a fonte da verdade** do que cobrar; Stripe e Asaas só cobram e avisam. Isso evita depender de recursos que só um dos dois tem (o Stripe tem cobrança por uso medido e proração automática; o Asaas trabalha com assinatura de valor fixo e cobranças avulsas).

```
organizations ─ billing_accounts (provider: asaas|stripe, provider_customer_id, tax_id, país, moeda)
              └ subscriptions (plano, ciclo, status, provider_subscription_id, period_start/end, spend_cap)
                 └ subscription_items (tipo: base|instance_slots|addon, quantidade, preço unitário)
usage_daily (org, project, date, instances_active, messages_sent, webhook_deliveries, media_gb, ...)
invoices_mirror (provider_invoice_id, status, valor, vencimento, pago_em, nota_fiscal_id)
billing_events (provider, provider_event_id UNIQUE, tipo, payload, recebido_em, processado_em)
```

### 6.3 Unidade de cobrança e proração

- **Slots de instância (pré-pago)**: o cliente contrata N instâncias (como na Z-API). A assinatura cobra `base + N × preço por instância` no início do ciclo. Criar uma instância além de N aumenta N (com confirmação no painel; recusado se o teto de gastos estiver ligado e o aumento passar dele).
- **Proração**: aumentar N no meio do ciclo cobra os dias restantes. No Stripe, `proration_behavior=create_prorations` na atualização da assinatura. No Asaas, o control plane calcula `(dias restantes / dias do ciclo) × preço × ΔN`, emite uma cobrança avulsa e atualiza o valor da assinatura para o próximo ciclo. Reduzir N vale a partir do próximo ciclo (sem reembolso proporcional; como Z-API e a maioria dos SaaS).
- **"Instância conectada"**: o slot é cobrado enquanto a instância existe; a palavra "conectada" do preço é marketing, não regra de medição (uma instância desconectada continua ocupando IP, memória e volume). Alternativa avaliada em D3: cobrar só dias em que a instância ficou `connected` em algum momento (mais justo, mais difícil de prever e explorável: o cliente desliga a instância para não pagar e mantém o IP).
- **Componentes por uso (pós-pago)**: mensagens acima da cota, mídia acima da cota, add-ons (IP fixo para webhooks, retenção maior). O worker de uso fecha `usage_daily` a partir dos contadores do motor (E11); no fim do ciclo, Stripe recebe *meter events* (ou itens de fatura) e o Asaas uma cobrança avulsa com o detalhamento. Excedente abaixo de R$ 5 acumula para o ciclo seguinte.
- **Teste grátis → pago**: ao assinar, o ciclo começa no dia do pagamento (sem proração do teste).
- **Anual**: 2 meses de desconto (como Z-API, que dá ~10%); só no C2.

### 6.4 Inadimplência (dunning)

| Dia (desde o vencimento) | Estado do projeto | O que acontece |
|---|---|---|
| D0 | ativo | falha no cartão: Stripe Smart Retries / nova tentativa do Asaas; boleto/Pix vencido: e-mail e WhatsApp (o próprio LiteZap envia) |
| D+3 | ativo com aviso | banner no painel, e-mail para Owner e Billing |
| D+7 | **envio suspenso** | o motor recusa novos envios da API (`sending_suspended`, E17); recepção, webhooks e leitura continuam (o cliente não perde mensagens de clientes dele) |
| D+14 | **projeto pausado** | Machine parada; sessões ficam no volume; painel só mostra cobrança e exportação (E21 permite subir o motor só para exportar) |
| D+30 | aguardando exclusão | aviso final; exportação continua disponível |
| D+45 | excluído | Machine, volume e snapshots destruídos; IPs para quarentena; registros fiscais mantidos |

Pagamento em qualquer momento antes de D+45 reativa tudo automaticamente (envio liberado na hora; projeto pausado volta em ~1 min, e as sessões provavelmente reconectam sem QR se a pausa foi curta (incerto, seção 2.7)). Prazos configuráveis por plano; Enterprise por contrato.

### 6.5 Webhooks de cobrança (idempotência)

- Endpoint por provedor; verificação de assinatura (Stripe: `Stripe-Signature`; Asaas: token de acesso configurado no webhook) antes de qualquer coisa.
- **Gravar e responder**: insere em `billing_events` com `UNIQUE(provider, provider_event_id)`; duplicata = responde 200 sem fazer nada. Responde 200 rápido e processa de forma assíncrona (o Asaas entrega pelo menos uma vez, pausa a fila depois de 15 falhas seguidas e guarda eventos por 14 dias; só o modo "sequencial" garante ordem [fontes: docs.asaas.com/docs/receba-eventos-do-asaas-no-seu-endpoint-de-webhook; docs.asaas.com/docs/fila-pausada]).
- **Ordem não confiável**: o processador nunca aplica o estado do evento às cegas; busca o objeto atual no provedor (assinatura, cobrança) e reconcilia. Transições de estado da assinatura no control plane são monotônicas por `period_end` e versão.
- **Efeitos colaterais idempotentes**: ações no data plane (liberar envio, iniciar Machine) são tarefas com chave `(projeto, ação, período)`.
- **Conciliação diária**: job que lista cobranças/assinaturas nos provedores e compara com o espelho; diferenças viram alerta. Alarme se a fila do Asaas estiver pausada.

### 6.6 Nota fiscal e impostos (só o mapa do problema)

- Serviço de software prestado a cliente no Brasil exige **NFS-e** municipal (regras e código de serviço variam por município; ISS). O Asaas emite NFS-e integrada às cobranças e assinaturas (R$ 0,49 por nota) por meio de `POST /v3/invoices` depois de configurar `fiscalInfo` do município [fonte: docs.asaas.com/docs/emitindo-notas-fiscais-de-servico]. Clientes pagos pelo Stripe no Brasil também precisam de NFS-e (emitida pelo Asaas ou por um emissor separado, como NFE.io/eNotas).
- Clientes no exterior: exportação de serviços (tratamento de ISS/PIS/COFINS diferente; câmbio). Reforma tributária (CBS/IBS) entra em transição a partir de 2026 e muda a emissão.
- Decisão de empresa emissora (Simples Nacional? Lucro Presumido?), município e contador: fora deste spec (D8).

### 6.7 Planos para parceiros

Inspirado no plano parceiro da Z-API (R$ 89,99 a partir de 10 instâncias, com API de gerenciamento de instâncias e cobrança pós-paga [fonte: z-api.io]): no Team, a organização pode ter muitos projetos (um por cliente final), cobrança consolidada pós-paga por instância-dia e a API de gerenciamento completa. White-label fica depois de C3.

## 7. Segurança e conformidade

### 7.1 Isolamento entre clientes

| Camada | Isolamento |
|---|---|
| Computação | um app Fly e uma Machine (microVM Firecracker) por projeto; nenhum processo de dois clientes na mesma VM |
| Rede interna | 6PN própria por projeto (`network` na criação do app); projetos não se enxergam |
| Dados | volume por projeto; nenhuma mensagem no Postgres do control plane |
| Segredos | segredos por app; token de operador e admin por projeto; chaves de dados por projeto |
| IP de saída | um IP por instância, nunca reatribuído a outro cliente antes da quarentena (5.5) |
| Painel | o painel do motor de um projeto só aceita o token daquele projeto; o login de um clique é emitido pelo control plane depois de checar o papel do membro |

A organização Fly dos projetos é separada da do control plane. Tokens do Fly usados pelos workers têm o menor escopo possível (tokens de organização com permissão só de apps/machines; o Fly oferece *macaroons* com restrições). O control plane nunca roda código do cliente.

### 7.2 LGPD

- **Papéis**: o cliente é o controlador dos dados das conversas; o LiteZap é operador. Contrato de processamento de dados (DPA) anexo aos termos.
- **Localização**: volumes e Machines em `gru` (São Paulo); Postgres do control plane em `sa-east-1` (São Paulo); logs no destino escolhido com região no Brasil ou EUA (decisão D9 — logs podem conter JIDs; preferir destino com região BR ou mascarar telefones antes de enviar). Snapshots do Fly ficam na mesma região do volume (incerto: confirmar na documentação).
- **Minimização**: o motor já evita telefones e tokens nas mensagens de erro. O control plane guarda só o hash do telefone (para a regra de IP e para detectar abuso), não o número em claro, salvo o que o cliente informar no cadastro.
- **Retenção**: log de mensagens conforme `message_retention_days` escolhido pelo cliente (padrão da nuvem: 90 dias, configurável até ilimitado no Team); mídia 7 dias (`media_retention_days`; limpeza automática é pré-requisito, item E15); logs de aplicação conforme o plano; snapshots conforme o plano.
- **Direitos do titular**: o cliente atende os titulares usando a API (apagar chat/mensagens — endpoint novo E16 — e exportar). O LiteZap atende pedidos do próprio cliente (dados de conta e cobrança).
- **Exportação e exclusão do projeto**: exportação a qualquer momento (E8). Excluir um projeto destrói Machine, volume e snapshots em até 7 dias (fila de exclusão com período de arrependimento de 7 dias, desligável); o control plane mantém só os registros fiscais e de cobrança pelo prazo legal.
- **Encarregado (DPO)** e canal de privacidade publicados no site. Registro das operações de tratamento.

### 7.3 Prevenção de abuso

O LiteZap é para conversas esperadas (atendimento, clientes, opt-in), como diz o spec do motor. Spam marca o IP e o número e prejudica o pool. Medidas:

- **KYC leve**: e-mail verificado e telefone verificado (SMS ou WhatsApp) para iniciar o teste; CPF/CNPJ obrigatório para pagar com Asaas (já é exigência de emissão de cobrança no Brasil); cartão internacional via Stripe com Radar. Um teste por pessoa/CPF/cartão/telefone (impressão digital de dispositivo opcional).
- **Termos de uso** proibindo envio em massa não solicitado, compra de listas, conteúdo ilegal e "maturação" artificial de números. Violação = suspensão.
- **Limites técnicos**: os pisos de ritmo e a rampa do motor ficam travados na nuvem (`unsafe_pacing` é do operador, E4). Teste grátis com cota baixa de mensagens e de destinatários novos por dia (ex.: 20/dia).
- **Sinais automáticos** (worker `abuse_watch`, por instância e por organização): taxa de `invalid_number` alta (lista comprada), proporção de destinatários novos sobre respostas, mensagens idênticas para muitos destinatários, `banned`/`temp_banned` repetidos, muitas instâncias novas pareadas e banidas em pouco tempo, *chargebacks*. Ações em degraus: alerta → redução de cota → pausa da fila do projeto (motor: E17, modo "envio suspenso") → suspensão do projeto com revisão humana.
- **Limites de API** no control plane (por chave e por IP) e no motor (já existem cotas de `check-numbers`; adicionar limite global de requisições por token, E18).
- **Denúncias**: endereço `abuse@` e formulário; resposta em 2 dias úteis.

### 7.4 Resposta a incidentes

- Severidades: **sev1** (control plane fora, ou muitos projetos fora, ou vazamento de dados), **sev2** (um projeto pago fora, onda de banimentos, falha de cobrança em massa), **sev3** (degradação).
- Plantão com escalonamento (seção 8.2); comunicação na página de status pública (`status.litezap.cloud`) com componentes: API de gerenciamento, painel, projetos (data plane), pool de IPs, cobrança.
- Vazamento ou suspeita: rotacionar tokens de operador e admin dos projetos afetados (o motor aceita token duplo durante a rotação), revogar chaves `lzm_`, rotacionar credenciais do provedor de proxy, notificar os clientes sem demora. Para os dados das conversas o cliente é o controlador e é dele a obrigação de comunicar a ANPD e os titulares (Resolução CD/ANPD nº 15/2024: 3 dias úteis a partir de saber que o incidente envolve dados pessoais; o dobro para agentes de pequeno porte); como operador, o LiteZap entrega ao cliente as informações para isso. Para os dados de conta e cobrança o LiteZap é controlador e comunica ele mesmo [fontes: legisweb.com.br/legislacao/?id=458235; mattosfilho.com.br/unico/regulamento-incidente-seguranca].
- Pós-incidente escrito, sem culpados, publicado para sev1.

### 7.5 Outros controles

- SSO/SAML e logs de auditoria (quem criou/excluiu projeto, viu o admin token, mudou plano) para Team+; o log de auditoria existe desde o C1 internamente, só a exibição é por plano.
- 2FA (TOTP) para todos os membros, obrigatório para Owner/Admin.
- Allowlist de IP por token (M11 do doc de paridade; E10) para clientes que pedem.
- Revisão de segurança do motor (o painel e a API ficam expostos à internet na nuvem, diferente do uso local) antes do C1: CSP, limites de corpo, `trusted_proxies` (o Fly envia `Fly-Client-IP`).

## 8. Operação

### 8.1 SLOs

| Indicador | Objetivo | Medição |
|---|---|---|
| Disponibilidade da API de gerenciamento e do painel | 99,5% mensal (C1–C2), 99,9% (C3) | sonda externa a cada 1 min |
| Disponibilidade do projeto (data plane) | 99,5% mensal Pro; 99,9% Team a partir do C3 | `GET https://<ref>.litezap.cloud/readyz` a cada 1 min, de fora do Fly |
| Reconexão depois de restart/upgrade | 99% das instâncias que estavam `connected` voltam a `connected` em ≤ 2 min | eventos `instance.status` |
| Reconexão depois de falha de proxy | ≤ 15 min até a troca automática de IP (C2) | idem + log de trocas |
| Provisionamento de projeto | p95 < 60 s | tarefas do control plane |
| Criação de instância até QR | p95 < 15 s | idem |
| Latência de webhook (evento → primeira tentativa) | p95 < 5 s | métricas do motor (E3) |

Fora do SLA: banimento, logout pelo celular, celular principal inativo, indisponibilidade do WhatsApp, manutenção programada avisada com 48 h. 99,5% permite ~3,6 h fora por mês; com **uma Machine e volume local por projeto**, a falha de um host do Fly é o risco dominante (recuperação por snapshot, RPO até 24 h, alvo de 30 min de recuperação), e por isso 99,9% só é prometido depois de C3 (Litestream e runbook testado). SLA com crédito como Supabase e ClickHouse (créditos de 10–30% da mensalidade conforme a faixa, limitados) [fontes: supabase.com/sla; clickhouse.com/legal/agreements/service-level-agreement].

### 8.2 Plantão e alertas

- C1: rodízio entre os fundadores, horário estendido (08–22h BRT) para sev2 e 24×7 para sev1; ferramenta de plantão do destino de logs (Better Stack/Grafana OnCall). C3: plantão formal com no mínimo 3 pessoas.
- O Prometheus gerenciado do Fly não tem alertas próprios [fonte: fly.io/docs/monitoring/metrics]; as regras rodam no Grafana do time (consultando o Prometheus do Fly) e em jobs do control plane.

| Alerta | Severidade |
|---|---|
| projeto pago com `/readyz` falhando por 3 min | sev2 (sev1 se ≥ 5% dos projetos) |
| % de instâncias em `reconnecting` no pool > 5% por 5 min | sev1 |
| pico de `banned`/`temp_banned` (2× a média de 30 dias) | sev2 |
| taxa de erro do provedor de proxy > 2% nas sondas | sev2 |
| falhas na Machines API / provisionamento > 3 seguidas | sev2 |
| fila do Asaas pausada, webhook do Stripe falhando, conciliação com diferença | sev2 |
| volume > 80%, memória da Machine > 85% por 15 min, restarts de worker em alta | sev3 (aciona redimensionamento) |
| snapshot diário ausente, certificado a 14 dias de expirar, estoque de IPs livres < 10% | sev3 |
| uso da cota de Machines da organização Fly > 80% | sev3 |

### 8.3 Capacidade

- **Cota do Fly**: o Fly limita cada organização a **50 Machines** por padrão (confirmado por funcionário em 2025-11-12; aumento por e-mail para billing@fly.io) [fonte: community.fly.io/t/is-the-maximum-of-50-machines-correct/26432]. Pedir aumento antes do beta e monitorar. Apps por organização: sem limite.
- **Ritmo da Machines API**: ~1 req/s por ação com rajadas de 3 (relato da comunidade, (incerto)) [fonte: community.fly.io/t/understanding-fly-io-api-rate-hard-limits/20636]. O worker de provisionamento usa um limitador e um pool de apps/volumes pré-criados se a criação ficar lenta (o Fly sugere pool pré-provisionado para criação instantânea [fonte: fly.io/docs/blueprints/per-user-dev-environments]).
- **Memória**: a tabela 2.6 é ponto de partida; medir RSS de supervisor e workers no C1 (com e sem *history sync*) e ajustar. Redimensionar automaticamente quando a memória passar de 85% por 15 min.
- **Volume**: começa em 1 GB por projeto Nano, extensão automática pelo control plane (o Fly cobra o tamanho provisionado). A limpeza de mídia (E15) é o que mantém o volume pequeno.
- **IPs**: compra antecipada mantendo 10–20% livres; prazo de entrega do fornecedor medido no C1.
- **Capacidade da região `gru`**: um único lugar físico; se o Fly ficar sem capacidade em `gru`, criação falha (há relatos de falha de criação de Machine; o provisionamento repete e alerta). Não há região alternativa no Brasil.

### 8.4 Modelo de custo por cliente

Premissas (todas **estimativas** salvo onde citado; câmbio usado: **R$ 5,50 por US$**, revalidar):

- Fly em `gru` (2.6); volume US$ 0,15/GB-mês; snapshots US$ 0,08/GB-mês com 10 GB/mês grátis na organização (ignorado no modelo, pior caso); saída de internet US$ 0,04/GB na América do Sul [fonte: docs.fly.io/about/pricing]; ~1,5 GB/instância/mês de tráfego Fly → proxy; certificado US$ 0,10/mês por hostname além dos 10 primeiros.
- IP: US$ 5,00/instância/mês (5.7).
- Logs: ~US$ 0,20/instância/mês no destino (depende do provedor escolhido; D9).
- Taxa de pagamento por fatura: Pix/boleto Asaas R$ 1,99; cartão Asaas 2,99% + R$ 0,49; Stripe cartão BR 3,99% + R$ 0,39 + 0,7% Billing (+2% se internacional).
- Custos fixos do control plane (não rateados abaixo): ~US$ 300/mês no C1 (2 Machines do control plane, Postgres gerenciado, destino de logs, suporte Standard do Fly US$ 29, e-mail, página de status) (estimativa).

| Cenário | Machine | Volume + snapshots | Saída + cert + logs | IPs | **Total/mês** | **Por instância** |
|---|---|---|---|---|---|---|
| A — 1 instância (Nano) | 5,96 | 0,23 | 0,36 | 5,00 | US$ 11,55 | **US$ 11,55 ≈ R$ 64** |
| B — 5 instâncias (Micro) | 10,82 | 0,69 | 1,40 | 25,00 | US$ 37,91 | **US$ 7,58 ≈ R$ 42** |
| C — 30 instâncias (Medium) | 46,00 | 3,45 | 7,90 | 150,00 | US$ 207,35 | **US$ 6,91 ≈ R$ 38** |
| D — 100 instâncias (XL) | 165,00 | 11,50 | 26,10 | 500,00 | US$ 702,60 | **US$ 7,03 ≈ R$ 39** |

Leitura:

- **O IP residencial é ~70% do custo a partir de 5 instâncias.** Negociar volume com o fornecedor de proxy pesa mais que otimizar Machine. Reservas do Fly (~40% em CPU/RAM) reduzem só a fatia menor.
- Um projeto de 1 instância custa ~R$ 64/mês, perto do preço de mercado; o primeiro slot precisa carregar o custo da Machine.
- Referência de mercado por instância/mês (2026-10-17): Z-API R$ 99,99 (parceiro R$ 89,99 a partir de 10), CloudZapi R$ 97 → R$ 59 com volume, Whapi.Cloud R$ 49 (promoção), W-API R$ 19,90–29,90, Zappy a partir de R$ 14,99 [fontes: z-api.io; cloudzapi.com; whapi.cloud/pt/price; w-api.app; zappy.api.br]. Os mais baratos, até onde se vê, não incluem IP residencial dedicado (incerto).

**Piso de preço sugerido** (margem de contribuição ≥ 30% sobre o custo variável, depois da taxa de pagamento):

| Faixa (por organização) | Custo variável/instância | Piso | Preço proposto (Pro) | Margem de contribuição no preço proposto |
|---|---|---|---|---|
| 1ª instância (inclui a Machine do projeto) | ~R$ 64 | R$ 95 | **R$ 99** | ~34% (Pix) |
| 2ª à 9ª | ~R$ 42 | R$ 62 | **R$ 79** | ~45% |
| 10ª à 49ª | ~R$ 38 | R$ 56 | **R$ 69** | ~43% |
| 50ª em diante | ~R$ 39 | R$ 57 | **R$ 59** | ~32% |

- **Team**: taxa de organização sugerida de **R$ 499/mês** (SSO, auditoria, suporte prioritário, SLA 99,9% a partir do C3) + as mesmas faixas por instância. (O Supabase Team custa US$ 599/mês [fonte: supabase.com/pricing].)
- **Desconto "traga seu proxy"** (opcional, D3): −R$ 25/instância para quem fornece o próprio proxy (o IP não é do pool, então não há risco de contaminação; o cliente assume o risco de ban).
- **Teste grátis**: custo de ~R$ 15 por teste de 7 dias (Machine Nano + IP de teste reaproveitado após quarentena curta, já que o sub-pool de teste não atende pagantes). Com conversão de 10%, ~R$ 150 de custo de aquisição por cliente pago (estimativa).
- Preços em USD para Stripe internacional: US$ 19 / 15 / 13 / 11 nas mesmas faixas (estimativa, arredondado).

## 9. Mudanças no motor para a nuvem (backlog)

Tudo abaixo entra no motor open source (serve também a quem faz self-host); nada é exclusivo da nuvem. Esforço: **P** (até 2 dias), **M** (até 1 semana), **G** (mais de 1 semana). "Marco" diz em que fase da nuvem o item é necessário.

| # | Mudança | Por quê | Esforço | Marco |
|---|---|---|---|---|
| E1 | **Config 100% por ambiente**: `instance_defaults`, retenções de webhook, `message_retention_days`, `media_retention_days` e limites hoje só no arquivo ganham `LITEZAP_*` (ou um `LITEZAP_CONFIG_JSON`) | a Machine é configurada só por env e segredos; sem arquivo no volume | P | C1 |
| E2 | **Endpoint de ops separado**: `ops_listen_addr` com `/healthz`, `/readyz` e `/metrics` sem token (hoje exigem admin fora de loopback) | checks do Fly e Prometheus sem segredo em `fly.toml` | P | C1 |
| E3 | **`/metrics` Prometheus** (já previsto na seção 9.4 do motor; não implementado) | métricas por projeto, alertas, painel | M | C1 |
| E4 | **Token de operador e settings travadas**: `operator_token` (aceita dois valores para rotação) com poder acima do admin; `locked_settings: [proxy_url, unsafe_pacing]` faz o admin receber `403 forbidden` nesses campos | o cliente pode ter o token admin sem mexer no IP nem nos pisos de ritmo | M | C1 |
| E5 | **Limite de instâncias**: `max_instances` (env), `POST /instances` acima responde `403 plan_limit` | o cliente com token admin não cria instâncias fora do plano | P | C1 |
| E6 | **Evento `instance.proxy_failed`** (WHK-9/M11) com contagem de falhas e último erro, além do `reason: proxy_unreachable` | quarentena de IP e troca automática | P | C1 |
| E7 | **Login de um clique emitido por terceiro**: `/app/api/session` aceitar um código assinado (HMAC com segredo do projeto, curto, uso único) emitido pelo control plane, não só pelo app de bandeja em loopback | reaproveitar o painel do motor dentro do painel da nuvem | M | C1 |
| E8 | **Exportação/importação**: `POST /v1/export` (admin) gera arquivo com backup online do SQLite, mídia e config, opcionalmente enviado a uma URL S3 pré-assinada; `litezap import` | backups lógicos, LGPD, migrar entre self-host e nuvem nos dois sentidos | M | C1 (exportação), C2 (importação) |
| E9 | **Logs em JSON** (`LITEZAP_LOG_FORMAT=json`) com `instance_id` e sem telefones | explorador de logs | P | C1 |
| E10 | **Allowlist de IP por token + `trusted_proxies`** (SEC-4/M11), lendo `Fly-Client-IP` | pedido comum de empresas | P | C2 |
| E11 | **Contadores de uso persistidos**: tabela diária por instância (enviadas, recebidas, entregas de webhook, bytes de mídia, minutos conectada) e `GET /v1/usage?from=&to=` (operador) | cobrança por uso confiável, independente de métricas | M | C1 |
| E12 | **Upgrade gracioso**: `/readyz` passa a 503 assim que o SIGTERM chega; shutdown cabe no `kill_timeout`; migrações compatíveis com uma versão para trás (regra de processo) | ondas de upgrade e rollback | P | C1 |
| E13 | **Watchdog de conexão zumbi** (`zombie_timeout`, pendente desde o marco 2b) | sockets "conectados" que não recebem nada são o pior caso para SLA | M | C1 |
| E14 | **API admin para plataformas** (M11): renomear instância, exclusão em lote, paginação de `GET /instances`, metadados livres por instância (`labels`) | control plane e parceiros | P | C2 |
| E15 | **Limpeza de mídia e retenção do log** (marco 2b) | custo de volume e LGPD | P | C1 |
| E16 | **Apagar chat/mensagens pela API** | direitos do titular (LGPD) | P | C2 |
| E17 | **Modo "envio suspenso"** por instância e global (`sending_disabled`, só operador): a fila aceita e segura, ou recusa com `403 sending_suspended` | cobrança atrasada e abuso, sem derrubar a recepção | P | C1 |
| E18 | **Limite global de requisições por token** (429) | proteção do projeto exposto | P | C2 |
| E19 | **Confirmação de entrega worker→supervisor** (limitação conhecida do README) | não perder eventos em crash | M | C3 |
| E20 | **Página de pareamento hospedada** (PAR-6/M11): link assinado de uso único que mostra o QR | parceiros embutem a conexão no software deles | M | C2 |
| E21 | **Projeto em modo "pausado"**: `serve` sobe sem iniciar workers (`LITEZAP_START_INSTANCES=false`) para exportação e diagnóstico | suspensão por cobrança sem apagar nada | P | C2 |

O `ContainerSpawner` previsto na decisão 4 do motor **não** é necessário: o `ProcessSpawner` roda dentro da Machine do projeto. Ele só volta à mesa se a decisão D2 mudar.

## 10. Roadmap

Marcos enxutos; cada um termina com clientes reais usando o que foi entregue.

### C1 — MVP pago (beta fechado, ~20 clientes)

Escopo:

- Motor: E1–E9, E11–E13, E15, E17.
- Control plane: contas (e-mail + senha + 2FA), uma organização por conta, papéis Owner/Developer, projetos e instâncias pela API de gerenciamento e pelo painel; provisionamento automático pelo `FlyProvider` (a aprovação do cliente no beta é manual, o provisionamento não); painel do motor embutido via login de um clique.
- Um provedor de proxy (decisão D7), pool com atribuição 1 IP por instância, quarentena manual com apoio de alertas.
- Cobrança: plano Pro único, checkout Asaas (Pix, boleto, cartão BR) e Stripe (cartão internacional), quantidade de instâncias contratada (slots), dunning básico (avisos, suspensão do envio, suspensão do projeto).
- Operação: snapshots diários, upgrades em ondas manuais (script), logs enviados ao destino escolhido (sem explorador no painel ainda; link para o projeto no destino só para a equipe), alertas básicos, página de status pública.

Critérios de aceite:

1. Do cadastro à primeira instância conectada em menos de 5 minutos; provisionamento do projeto p95 < 60 s em 20 criações seguidas.
2. Cada instância sai por um IP brasileiro diferente, verificado por um endpoint de eco pelo proxy, e nenhum IP serve a dois clientes (consulta no Postgres + teste automatizado).
3. Um upgrade do motor aplicado a todos os projetos do beta com todas as instâncias de volta a `connected` em até 2 minutos, sem novo QR.
4. Pagamento por Pix, boleto e cartão BR e por cartão internacional cria/atualiza a assinatura; webhook duplicado ou fora de ordem não altera o resultado (teste com reenvio de eventos).
5. Atraso de pagamento leva a aviso → envio suspenso → projeto parado nos prazos configurados, e o pagamento reativa tudo sem intervenção.
6. Restaurar um snapshot num projeto novo recupera o log de mensagens e os webhooks.
7. Custo real por instância (fatura Fly + proxy + taxas) medido e dentro de ±20% do modelo da seção 8.4.

### C2 — Self-service

Escopo:

- Teste grátis de 7 dias com KYC leve; assistente de criação de projeto; vários projetos e organizações por conta; convites e todos os papéis.
- Planos Pro e Team, componentes por uso (mensagens, mídia), teto de gastos, alertas de cobrança, página de uso, faturas.
- Explorador de logs e painel de métricas por projeto; página de estado por projeto.
- API de gerenciamento pública documentada (OpenAPI) + CLI `litezap cloud` (login, `projects create|list|delete`, `instances create|qr|delete`, `usage`), chaves `lzm_` com escopo por projeto. Página de pareamento hospedada (E20) para parceiros.
- Motor: E8 (importação), E10, E14, E16, E18, E20, E21.
- Ondas de upgrade automatizadas com gates por métricas; troca automática de IP com quarentena (5.5); escolha de UF/DDD.

Critérios de aceite:

1. Um usuário novo, sem contato com a equipe, cria conta, paga e conecta um número.
2. Um script de parceiro cria 10 projetos com 1 instância cada pela API de gerenciamento e obtém os links de pareamento, sem usar o painel.
3. Com o teto de gastos ligado, nenhum excedente é cobrado e o cliente recebe os alertas de 80% e 100%.
4. A onda de upgrade para sozinha quando o canário piora a taxa de reconexão (teste com uma versão sabotada em ambiente de staging).
5. Um IP marcado como ruim é trocado automaticamente, a instância reconecta e o IP antigo vai para quarentena.

### C3 — Escala e operação

Escopo:

- Team completo: SSO/SAML, logs de auditoria visíveis, papéis por projeto, adiamento de upgrades, SLA 99,9% com créditos.
- Enterprise: organização Fly dedicada, contrato, suporte com SLA; desenho do BYOC.
- Litestream do `litezap.db` (RPO de minutos para o log), exportação agendada para o bucket do cliente.
- Segundo provedor de proxy (redundância e preço), IPs móveis como add-on.
- Motor: E19; avaliação de `ContainerSpawner` se projetos acima de 128 instâncias forem comuns.
- Capacidade: automação de redimensionamento, relatórios de custo por cliente, revisão de preço.

Critérios de aceite:

1. 500 projetos ativos sem trabalho manual recorrente acima de 2 h/semana para a operação.
2. Disponibilidade medida do data plane ≥ 99,9% por 3 meses nos projetos Team.
3. Falha simulada de um host Fly: projetos afetados recuperados de snapshot em até 30 minutos (runbook testado).
4. Falha total de um provedor de proxy: instâncias migradas para o segundo provedor em até 1 h, com aviso aos clientes.

### Depois de C3

BYOC, multi-região (outra região só se houver demanda fora do Brasil), white-label para revendas, plano Free permanente se o custo por instância cair (D4).

## 11. Decisões em aberto

Cada decisão: opções, recomendação e o que depende dela. Nenhuma está fechada.

**D1 — Fly: app por projeto × Machine por projeto num app compartilhado**
- Opções: (a) um app por projeto, rede 6PN própria; (b) um app compartilhado com uma Machine por projeto; (c) uma organização Fly por cliente.
- Recomendação: **(a)**. É a orientação do Fly para plataformas, isola segredos e rede, separa logs e escala, e a ferramenta do Fly não lida bem com milhares de Machines num app. (b) colocaria todos os segredos no mesmo app (o token de um vaza para todos) e todos na mesma rede. (c) só para Enterprise dedicado/BYOC.
- Depende disso: provisionamento (3.3), certificados por hostname, consulta de logs/métricas por `app`, preço (certificados US$ 0,10 após os 10 primeiros).

**D2 — Modelo de processo na nuvem**
- Opções: (a) `ProcessSpawner` dentro da Machine do projeto (supervisor + workers como processos); (b) `ContainerSpawner`, uma Machine por instância com volume compartilhado.
- Recomendação: **(a)** até C3. Zero mudança no motor, isolamento entre clientes já é por Machine. (b) só se projetos com mais de ~128 instâncias forem comuns, e exige resolver mídia por arquivo entre Machines (o README já aponta essa limitação).
- Depende disso: tamanhos de compute (2.6), backlog do motor, custo.

**D3 — Preço e unidade de cobrança**
- Opções de unidade: (a) slots de instância pré-pagos (como Z-API); (b) instância-dia medida, pós-paga; (c) instância-dia só quando `connected`. Opções de preço: paridade com Z-API (R$ 99) × abaixo (R$ 69–79) × premium.
- Recomendação: **(a) slots pré-pagos** no C1 (funciona igual em Asaas e Stripe, previsível), com faixas **R$ 99 / 79 / 69 / 59** (8.4) e Team com taxa de R$ 499. (b) para parceiros no Team. Mensagens contam só envios; cota generosa para não virar atrito. Desconto "traga seu proxy" a decidir depois de medir demanda.
- Depende disso: página de preços, modelo de dados de cobrança (6.2), teto de gastos, margem.

**D4 — Teste grátis e plano Free**
- Opções: (a) teste de 7 dias sem cartão, 1 instância; (b) teste com cartão; (c) Free permanente com Machine suspensa quando ociosa; (d) Free permanente "só envio".
- Recomendação: **(a)** com KYC leve (e-mail + telefone) e sub-pool de IPs de teste. Free permanente não no lançamento (cada projeto custa Machine + IP); o grátis permanente é o self-host. Reavaliar (c)/(d) quando houver dados de custo.
- Depende disso: abuso (7.3), custo de aquisição, pausa de projetos (2.7).

**D5 — O cliente recebe o token admin?**
- Opções: (a) nunca: só tokens de instância e a API de gerenciamento; (b) sim, um token admin "de cliente" com `proxy_url`/`unsafe_pacing` travados e `max_instances` (E4, E5), além do token de operador só nosso; (c) sim, irrestrito.
- Recomendação: **(b)**. Mantém a promessa "é o mesmo LiteZap do self-host" (painel, webhooks globais, `/v1/events`, MCP admin) sem abrir o pool de IPs. (c) é inviável (o cliente trocaria o proxy e desligaria os pisos). Mostrar o token admin só para Owner/Admin e registrar no log de auditoria.
- Depende disso: E4/E5 no motor (bloqueia C1), papéis (2.2), painel.

**D6 — Postgres do control plane**
- Opções: (a) Supabase Pro em `sa-east-1` (US$ 25/mês, backups diários, PITR pago à parte); (b) Neon em `aws-sa-east-1` (por uso, US$ 0,106–0,222/CU-hora, restauração até 30 dias no Scale); (c) Fly Managed Postgres (a partir de US$ 38/mês; presença em `gru` (incerto)).
- Recomendação: **(a) Supabase** no C1: São Paulo confirmado, preço fixo baixo, PITR disponível quando necessário, e o time já conhece. (c) passa a ser atraente se confirmar `gru` (latência menor, mesma rede), mas apps em redes 6PN isoladas não alcançam o MPG, o que não importa aqui porque só o control plane acessa o banco.
- Depende disso: LGPD (localização), backups do control plane, custos fixos.

**D7 — Fornecedor de proxy**
- Opções: IPRoyal, Proxy-Cheap, Bright Data, móvel BR (Coronium/ProxyBrazil), outros (5.3).
- Recomendação: **IPRoyal** no C1 (com acordo escrito de uso em SaaS), Proxy-Cheap testado em paralelo pelas cidades fora de SP, **Bright Data** como segundo fornecedor no C3. Móvel só como add-on Enterprise.
- Depende disso: custo por instância (~70% do custo), casamento de UF/DDD, API de troca de IP, contrato.

**D8 — Entidade, moeda e nota fiscal**
- Opções: (a) só empresa brasileira, Asaas + Stripe Brasil, preços em BRL e USD; (b) empresa brasileira + entidade nos EUA para Stripe US.
- Recomendação: **(a)** até haver receita internacional relevante. NFS-e pelo Asaas para todos os clientes brasileiros (inclusive os que pagam pelo Stripe). Regime tributário e município com contador.
- Depende disso: taxas (6.1), emissão de notas, contratos.

**D9 — Destino de logs e métricas**
- Opções: Better Stack, Axiom, Grafana Cloud (Loki), ClickHouse (próprio ou Cloud) para logs; Prometheus do Fly + Grafana para métricas.
- Recomendação: **Grafana Cloud ou Better Stack** no C1 (preço inicial baixo, alertas e plantão juntos); escolher pela região de armazenamento (preferir dados no Brasil ou mascarar telefones antes do envio). Avaliar ClickHouse para o explorador de logs do cliente no C2 se o volume crescer.
- Depende disso: explorador de logs (C2), LGPD, custo fixo.

**D10 — Estratégia de região**
- Opções: (a) só `gru`; (b) `gru` + uma região nos EUA para clientes internacionais; (c) multi-região com failover.
- Recomendação: **(a)**. O público é brasileiro, os IPs são brasileiros, e o volume local impede failover simples. Reabrir só com demanda real fora do Brasil.
- Depende disso: SLA (8.1), LGPD, preço (gru é a região mais cara do Fly).

**D11 — Domínio e roteamento**
- Opções: (a) `<ref>.litezap.cloud` com certificado por app; (b) gateway com curinga e `fly-replay`; (c) domínio próprio do cliente (add-on).
- Recomendação: **(a)** no C1; (c) como add-on no C2 (o Supabase cobra US$ 10/mês por domínio próprio). (b) não serve para a API por causa do limite de 1 MB do `fly-replay`.
- Depende disso: provisionamento, `public_url`, URLs assinadas de mídia.

**D12 — Modelo de suporte**
- Opções: (a) e-mail + base de conhecimento; (b) + WhatsApp (natural para o público e o produto); (c) + canal dedicado (Slack/WhatsApp de grupo) no Enterprise.
- Recomendação: **(a) para Pro, (b) para Team, (c) Enterprise**; comunidade (Discord/GitHub Discussions) para o open source. Tempos de resposta na tabela 2.5. No C1, suporte direto dos fundadores para todos os clientes do beta.
- Depende disso: custo fixo, planos, plantão.

**D13 — Painel: reaproveitar o do motor × painel novo**
- Opções: (a) painel da nuvem (contas, cobrança, uso, logs) + painel do motor embutido por login de um clique; (b) reescrever tudo num só painel.
- Recomendação: **(a)**. Toda melhoria do painel open source chega à nuvem; o custo é a integração visual (mesma identidade, links entre os dois) e E7 no motor.
- Depende disso: E7, roadmap C1.

**D14 — Pausa do teste e reativação**
- Opções: projeto do teste expirado (a) excluído após 30 dias; (b) excluído após 7 dias; (c) mantido pausado até 1 ano (como o Supabase restaura projetos Free pausados por até 1 ano).
- Recomendação: **(a)**. O volume custa pouco, mas o IP de teste fica preso e a sessão provavelmente não volta depois de semanas.
- Depende disso: custo do teste, LGPD (retenção).

## 12. Mapa Supabase/ClickHouse → LiteZap

O que cada padrão do Supabase Cloud e do ClickHouse Cloud vira no LiteZap, e quando entra. Descrições dos dois produtos resumidas das páginas públicas (fontes na seção 13).

| Padrão | Supabase / ClickHouse | LiteZap Cloud | Quando |
|---|---|---|---|
| Hierarquia | org → projetos (Supabase); org → services (ClickHouse) | organização → projetos → instâncias (2.1) | C1 (uma org por conta), C2 (várias) |
| Papéis | Owner/Administrator/Developer/Read-only, papéis por projeto no Team+ (Supabase); Admin/Billing/Member + papéis por serviço (ClickHouse) | Owner/Admin/Developer/Billing/Read-only (2.2) | C1: Owner/Developer; C2: todos; C3: por projeto |
| Chaves por projeto | chaves de API do projeto; tokens pessoais para a Management API | token admin do projeto, tokens `lzi_` do motor, chaves `lzm_` do control plane (2.3) | C1 |
| Assistente de criação | nome, região, compute, plano | nome, região (`gru`), tamanho, plano; p95 < 60 s; página de estado do projeto (2.4) | C1 (simples), C2 (completo) |
| Planos | Free / Pro US$ 25 / Team US$ 599 / Enterprise (Supabase); Basic / Scale / Enterprise (ClickHouse) | Teste / Pro / Team / Enterprise (2.5, 8.4) | C1: Pro; C2: Team e teste; C3: Enterprise |
| Componentes por uso | egress, armazenamento, MAU além da cota (Supabase); compute por minuto + armazenamento + egress (ClickHouse) | slots de instância + mensagens, mídia, add-ons de IP (6.3) | C1: slots; C2: uso |
| Teto de gastos e alertas | spend cap ligado por padrão no Pro (Supabase) | teto ligado por padrão no Pro; alertas 50/80/100% (2.5) | C2 |
| Painel de uso | página de uso por org/projeto | uso diário por projeto e instância (E11) | C2 |
| Tamanhos de compute | add-ons Micro US$ 10 a 16XL (Supabase); tamanhos em passos de 8 GiB, escala vertical automática no Scale (ClickHouse) | Nano a XL (2.6), redimensionamento automático com aviso | C1: manual; C3: automático |
| Pausa por ociosidade | Free pausado após 7 dias de pouca atividade, restaurável por 1 ano (Supabase); serviço ocioso para de cobrar compute (ClickHouse) | **não** pausa projeto pago (socket precisa ficar aberto); só o teste expirado pausa (2.7) | C2 |
| Backups e PITR | diário 7/14/30 dias por plano, PITR pago à parte (Supabase); backup por tier (ClickHouse) | snapshots diários 7/30 dias, exportação lógica, Litestream no Team (3.8) | C1: snapshots; C3: Litestream |
| BYOC / dedicado | BYOC: data plane na conta AWS/GCP/Azure do cliente, control plane do fornecedor, contrato comprometido (ClickHouse) | org Fly dedicada (C3); BYOC em org Fly do cliente, depois outras nuvens (2.8) | C3 / depois |
| Self-host grátis | Supabase open source auto-hospedado | `litezap serve` / Docker, mesmo painel (2.9) | já existe |
| Explorador de logs | Logs Explorer, retenção 1/7/28/90 dias por plano (Supabase) | explorador por projeto, retenção por plano (3.9) | C2 |
| Métricas | relatórios e métricas por projeto | `/metrics` do motor + Prometheus do Fly + gráficos no painel (3.9) | C1: interno; C2: no painel |
| Página de status | status page pública | `status.litezap.cloud` + estado por projeto | C1 |
| SLA por tier | 99,9% só Enterprise (Supabase); 99,9% com créditos (ClickHouse) | 99,5% Pro; 99,9% Team a partir do C3 (8.1) | C1 / C3 |
| Suporte por tier | comunidade → e-mail → SLA → TAM | comunidade → e-mail → prioritário + WhatsApp → dedicado (D12) | C1 |
| SSO/SAML | Team (Supabase, "contact us"); Enterprise (ClickHouse) | Team+ | C3 |
| Logs de auditoria | audit logs no Team+ (Supabase); activity log exportável via API (ClickHouse) | gravado desde o C1; visível e exportável no Team+ | C1 interno; C3 visível |
| API de gerenciamento | `api.supabase.com/v1` com tokens pessoais ou OAuth; `api.clickhouse.cloud/v1` com OpenAPI e chaves por org | `api.litezap.cloud/v1` com OpenAPI, chaves `lzm_` com escopo | C1 interno; C2 público |
| CLI / IaC | `supabase` CLI; provedor Terraform do ClickHouse | `litezap cloud …` (subcomando do mesmo binário, só cliente HTTP); Terraform depois de C3 | C2 |
| Parceiros | (ClickHouse/Supabase: integrações OAuth) | API de gerenciamento + página de pareamento hospedada + cobrança consolidada (6.7), resposta ao Partner API da Z-API | C2 |
| Redes privadas | PrivateLink/peering no Scale+ (ClickHouse) | allowlist de IP por token (E10); IP fixo de saída para webhooks (add-on) | C2 |

A CLI ser um subcomando do binário open source (`litezap cloud`) segue o padrão do Supabase (CLI única para local e nuvem); o código do subcomando é só um cliente da API pública do control plane, sem nada fechado.

## 13. Fontes

Consultadas em 2026-10-17. Preços em US$ salvo indicação.

Fly.io

- Preços (Machines, multiplicador de região, volumes, snapshots, banda, IPs, certificados, reservas, suporte): https://docs.fly.io/about/pricing
- Um app por cliente: https://docs.fly.io/machines/guides-examples/one-app-per-user-why
- Redes privadas customizadas (`network` em `POST /v1/apps`): https://docs.fly.io/networking/custom-private-networks
- Rede privada (6PN): https://docs.fly.io/networking/private-networking
- `fly-replay` (limite de 1 MB, replays entre redes): https://docs.fly.io/networking/dynamic-request-routing
- Machines API: https://docs.fly.io/machines/api/machines-resource/
- Suspend/resume: https://docs.fly.io/reference/suspend-resume/
- Snapshots de volume: https://docs.fly.io/volumes/snapshots/
- IPs de saída estáticos: https://docs.fly.io/networking/egress-ips ; https://community.fly.io/t/static-egress-ips-for-machines/22004
- Configuração (estratégias de deploy, checks): https://docs.fly.io/reference/configuration/
- Métricas (Prometheus gerenciado): https://docs.fly.io/monitoring/metrics/
- Log shipper: https://github.com/superfly/fly-log-shipper
- Limite de 50 Machines por organização: https://community.fly.io/t/is-the-maximum-of-50-machines-correct/26432
- Ritmo da Machines API (relato, incerto): https://community.fly.io/t/understanding-fly-io-api-rate-hard-limits/20636
- Ambientes pré-provisionados: https://www.fly.io/docs/blueprints/per-user-dev-environments/
- Managed Postgres: https://docs.fly.io/mpg ; https://community.fly.io/t/managed-postgres-additional-region-support-ams-nrt-sin-sjc/25882

Proxies

- IPRoyal: https://iproyal.com/static-residential-proxies/ ; https://docs.iproyal.com/proxies/isp-static-residential ; https://docs.iproyal.com/proxies/isp/api/orders ; https://iproyal.com/acceptable-use-policy/ ; https://docs.iproyal.com/proxies/residential/proxy/rotation
- Bright Data: https://brightdata.com/pricing/proxy-network/isp-proxies ; https://brightdata.com/locations/br ; https://brightdata.com/trustcenter/acceptable-use-policy-bright-data
- Oxylabs: https://oxylabs.io/pricing/isp-proxies
- Proxy-Cheap: https://www.proxy-cheap.com/pricing ; https://www.proxy-cheap.com/locations/brazil
- SOAX: https://soax.com/pricing
- Webshare: https://www.webshare.io/static-residential-proxy
- Rayobyte: https://rayobyte.com/products/isp-proxies/
- Coronium (móvel BR): https://www.coronium.io/mobile-proxies/brazil
- ProxyBrazil: https://proxybrazil.com
- Comparativo Brasil (Proxyway, 2026-03-16): https://proxyway.com/proxy-locations/brazil-proxy
- NetNut (desmantelamento): https://bleepingcomputer.com/news/security/netnut-proxy-network-disrupted-2-million-infected-devices-cut-off
- Consumo de dados do WhatsApp (blogs, anedótico): https://roamless.com/blog/how-much-data-does-whatsapp-use ; https://www.firsty.app/post/how-much-data-does-whatsapp-use

IP de datacenter e WhatsApp (anedótico)

- https://github.com/arslanstack/whatsapp-baileys
- https://station.railway.com/questions/railway-blocks-websockets-9ac08fce
- https://support.whapi.cloud/help-desk/blocking/using-residential-proxies-with-whapi.cloud
- https://github.com/tulir/whatsmeow/discussions/979
- Logout de aparelhos vinculados por inatividade (terceiro, incerto): https://green-api.com/en/docs/faq/why-does-the-crash-happen/

Pagamentos

- Stripe Brasil: https://stripe.com/br/pricing
- Asaas, preços: https://www.asaas.com/precos-e-taxas
- Asaas, webhooks: https://docs.asaas.com/docs/receba-eventos-do-asaas-no-seu-endpoint-de-webhook ; https://docs.asaas.com/docs/fila-pausada
- Asaas, NFS-e: https://docs.asaas.com/docs/emitindo-notas-fiscais-de-servico

Postgres

- Neon (regiões e preços): https://neon.com/docs/introduction/regions ; https://neon.com/pricing
- Supabase (regiões): https://supabase.com/docs/guides/platform/regions

Supabase Cloud

- Preços e planos: https://supabase.com/pricing
- Pausa de projetos Free: https://supabase.com/docs/guides/platform/free-plan-pausing
- Backups e PITR: https://supabase.com/docs/guides/platform/backups
- Controle de acesso: https://supabase.com/docs/guides/platform/access-control
- SLA: https://supabase.com/sla
- Management API: https://supabase.com/docs/reference/api/introduction

ClickHouse Cloud

- Tiers: https://clickhouse.com/blog/evolution-of-clickhouse-cloud-new-features-superior-performance-tailored-offerings ; https://clickhouse.com/pricing
- Modelo de cobrança: https://clickhouse.com/docs/cloud/manage/billing/overview
- Papéis do console: https://clickhouse.com/docs/cloud/security/console-roles
- BYOC: https://clickhouse.com/docs/cloud/reference/byoc
- Cloud API: https://clickhouse.com/docs/cloud/manage/api/api-overview
- Activity log: https://clickhouse.com/docs/cloud/security/audit-logging
- SLA: https://clickhouse.com/legal/agreements/service-level-agreement

Concorrentes (preço por instância)

- Z-API: https://www.z-api.io/
- W-API: https://w-api.app/
- Whapi.Cloud: https://whapi.cloud/pt/price
- CloudZapi: https://cloudzapi.com/
- Zappy: https://zappy.api.br/
- Whazap Cloud: https://whazap.cloud/planos

LGPD

- Resolução CD/ANPD nº 15/2024 (comunicação de incidente de segurança): https://www.legisweb.com.br/legislacao/?id=458235 ; resumo: https://www.mattosfilho.com.br/unico/regulamento-incidente-seguranca/ ; prazo para pequeno porte: https://confidata.com.br/blog/comunicacao-incidente-seguranca-anpd (confirmar no texto oficial)
