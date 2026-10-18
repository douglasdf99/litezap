# LiteZap Cloud — Design (Peça #3)

Data: 2026-10-18
Status: rascunho para revisão (nenhuma decisão de "Decisões em aberto" está fechada)
Revisão 2 (2026-10-18): hospedagem trocada de Fly.io para **AWS `sa-east-1` (São Paulo)**, na conta AWS existente; sem domínio próprio por enquanto (domínio temporário, seção 3.7); gateways de pagamento movidos para o **fim do C1** (o beta começa com provisionamento manual e cobrança manual).
Base: motor até o marco 4 + app local v1 (branch `integration/preview`). Specs relacionados: `2026-10-17-litezap-engine-design.md` (decisões 7–9, seções 4.6 e 9) e `2026-10-17-zapi-parity.md` (marco M11, branch `docs/zapi-parity`).

Convenções deste documento:

- **[fonte]** links para a página pública consultada em 2026-10-17/18. Preços mudam: revalidar antes de publicar a página de preços.
- **(estimativa)** número calculado por nós, não medido.
- **(incerto)** informação que não conseguimos confirmar numa fonte primária.

## 1. Contexto, objetivos e não objetivos

O LiteZap segue o modelo Supabase (decisão 7 do motor): o mesmo binário open source (Apache-2.0) roda no PC (app local), no servidor do próprio usuário (self-host) e na nuvem gerenciada. O que é fechado é só o **control plane** da nuvem: contas, provisionamento, cobrança, pool de IPs de saída e operação.

A nuvem deve parecer, para quem já usou, com o **Supabase Cloud** e o **ClickHouse Cloud**: organização → projetos, assistente de criação com região e plano, projeto pronto em menos de um minuto, painel por projeto com logs, métricas e uso, planos com cotas e componentes por uso, teto de gastos, API de gerenciamento e CLI.

### 1.1 Objetivos

1. **Projeto pré-instalado por cliente**: no C1, cada projeto é uma instância EC2 Graviton pequena e isolada, com volume EBS próprio, rodando o container do binário `litezap` sem modificações. O cliente recebe uma URL HTTPS (domínio temporário no C1, seção 3.7), tokens de instância e o painel.
2. **Estabilidade equivalente à Z-API**: sockets do WhatsApp sempre conectados, reconexão automática, upgrades sem perder sessão.
3. **IP de saída brasileiro e exclusivo por instância**, nunca compartilhado entre clientes, fixo durante a vida da instância (decisão 8 do motor).
4. **Cobrança em reais e em dólar**: Pix, boleto e cartão nacional via Asaas; cartão internacional via Stripe. Preço por instância conectada por mês, com componentes por uso.
5. **API de gerenciamento + CLI** para criar projetos e instâncias por programa, que é o que parceiros e integradores (o "Partner API" da Z-API, PAR-1…6 do doc de paridade) precisam.
6. **Dados no Brasil** (região AWS `sa-east-1`, São Paulo) e fluxo de exportação/exclusão alinhado à LGPD.
7. **Operação enxuta**: um time pequeno opera a nuvem; tudo que é repetitivo é automatizado no control plane.

### 1.2 Não objetivos (por enquanto)

- Multi-região ativa ou failover entre regiões. Só `sa-east-1` no início (com as três zonas de disponibilidade da região).
- Escalar um projeto horizontalmente (vários servidores servindo o mesmo projeto). Um projeto = um supervisor; para crescer, aumenta-se o tamanho da instância EC2 ou cria-se outro projeto.
- `ContainerSpawner` (um container por worker). O `ProcessSpawner` atual já isola workers em processos dentro do container do projeto; revisitar só se o tamanho do servidor virar limite (seção 11, decisão D2).
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
- **Projeto**: um ambiente LiteZap isolado = um servidor (EC2 no C1; container num host compartilhado a partir do C2 para projetos pequenos, seção 3.12) com um volume EBS. Tem `ref` curto e aleatório (`lz-k3f9q2`), nome, região, tamanho de compute, versão do motor e uma URL HTTPS própria. Casos de uso de vários projetos: produção e homologação, um projeto por cliente final de um parceiro, separar números de áreas diferentes.
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
| **Chave de gerenciamento** (`lzm_…`) | API do control plane | membro Admin/Developer, por organização, com escopo opcional por projeto | criar projetos, instâncias, ler uso; CLI; parceiros |
| **Token admin do projeto** | API do motor do projeto | o control plane, no provisionamento | o painel e o control plane falam com o motor; entregue ao cliente só conforme a decisão D5 |
| **Token de instância** (`lzi_…`) | API do motor, só aquela instância | o motor (`POST /instances`) | o cliente integra com a sua aplicação, MCP, n8n etc. |

Sobre o token admin: hoje ele pode tudo, inclusive `settings.proxy_url` e `unsafe_pacing`. Na nuvem esses dois campos são do operador (o IP de saída é do nosso pool e o ritmo protege o pool). Por isso o motor ganha um **token de operador** e uma lista de **settings travadas** (seção 9, item E4). Com isso o cliente pode receber um token admin "de cliente" sem conseguir trocar o proxy nem desligar os pisos de ritmo.

### 2.4 Criação de projeto

Assistente em três passos, como "New project" do Supabase e "Create service" do ClickHouse:

1. Organização e nome do projeto.
2. Região (`São Paulo (sa-east-1)`; outras aparecem desabilitadas com "em breve") e tamanho de compute (padrão pelo plano, seção 2.6).
3. Plano (se a organização ainda não tem) e pagamento; no período de teste, só confirmação de e-mail e telefone (seção 7.3).

Provisionamento alvo: no C1 o provisionamento é **disparado manualmente** pela equipe (um comando da ferramenta interna, depois de aprovar o cliente do beta), e leva ~2–3 min (boot da EC2 a partir da AMI pronta, pull da imagem, certificado). A partir do C2, self-service com **p95 < 90 s** usando um pequeno estoque de projetos pré-provisionados parados (seção 3.3); o "menos de um minuto" do Supabase vira meta do C3 com hosts compartilhados (criar um container num host já ligado leva segundos). O painel mostra uma página de estado do projeto com cada passo (como a tela "Setting up project" do Supabase) e, ao final, o botão "Conectar número" que leva direto ao QR da primeira instância.

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

Equivalente aos "compute add-ons" do Supabase e aos tamanhos de serviço do ClickHouse. No C1 o tamanho é o tipo da EC2 Graviton do projeto; cada instância de WhatsApp consome a memória de um processo worker.

| Tamanho | EC2 | Instâncias recomendadas | US$/hora em `sa-east-1` | US$/mês (730 h) | Observação |
|---|---|---|---|---|---|
| Nano | t4g.nano, 2 vCPU, 0,5 GiB | 1 (experimental) | 0,0067 | 4,89 | só se as medições do C1 mostrarem que SO + Docker + Caddy + motor cabem com folga; candidato ao teste grátis |
| Micro | t4g.micro, 2 vCPU, 1 GiB | até 3 | 0,0134 | 9,78 | padrão do Pro com poucas instâncias |
| Small | t4g.small, 2 vCPU, 2 GiB | até 10 | 0,0268 | 19,56 | |
| Medium | t4g.medium, 2 vCPU, 4 GiB | até 25 | 0,0536 | 39,13 | |
| Large | t4g.large, 2 vCPU, 8 GiB | até 60 | 0,1072 | 78,26 | Team |
| XL | t4g.xlarge, 4 vCPU, 16 GiB | até 120 | 0,2144 | 156,51 | Team/Enterprise; `m7g.large` (8 GiB, 0,1301/h) ou `r7g.large` (16 GiB, 0,1704/h) se a CPU sustentada passar da linha de base do `t4g` |

Preços on-demand Linux da AWS para `sa-east-1` (~1,6× `us-east-1`), arquivo de preços oficial publicado em 2026-10 [fonte: aws.amazon.com/ec2/pricing/on-demand, ver seção 13]. `t4g` em modo `unlimited`: uso acima da linha de base custa US$ 0,04 por vCPU-hora excedente. Savings Plan de computação de 1 ano sem pagamento antecipado reduz ~34% (ex.: t4g.micro 0,0088/h) e entra quando a base estabilizar.

Premissas (estimativa, **a medir no C1**): SO (Amazon Linux 2023) + Docker + Caddy + SSM Agent ~300–400 MB; supervisor ~60–100 MB; worker conectado ~40–80 MB em regime, com picos no *history sync* do pareamento (o motor limita cada worker a `worker_memory_max_mb`, padrão 512 MB, que na nuvem cai para 256 MB). As faixas acima reservam ~100–150 MB por instância mais o supervisor e o SO. O control plane sobe o tamanho (com aviso e preço mostrado antes) quando o número de instâncias passa da faixa; ou o cliente escolhe manualmente. Trocar o tipo da EC2 exige parar e iniciar a instância (~1–2 min fora do ar, sessões voltam sem QR), feito na janela de manutenção.

### 2.7 Pausa de projetos ociosos e o teste grátis

O Supabase pausa projetos Free após uma semana de inatividade e o ClickHouse suspende serviços ociosos para não cobrar compute. Para o LiteZap, **pausar quebra o produto**: um número só recebe mensagens se o socket estiver conectado, e um aparelho vinculado parado por muito tempo é deslogado pelo WhatsApp (relatos falam em ~14 dias sem uso do celular principal e ~30 dias de inatividade do aparelho vinculado; (incerto), sem página oficial encontrada [fonte: green-api.com/en/docs/faq/why-does-the-crash-happen]).

Por isso:

- **Projetos pagos nunca pausam** (servidor sempre ligado, sem desligamento por ociosidade).
- **Teste grátis**: 7 dias, 1 projeto, 1 instância, servidor sempre ligado durante o teste. Ao fim, sem plano, o projeto entra em **pausa**: a EC2 é parada (paga-se só o EBS e o IPv4 elástico, se mantido; a sessão fica no volume), e o painel mostra "assine para retomar". Retomar dentro de 14 dias costuma reconectar sem QR (incerto, depende do WhatsApp). Após 30 dias pausado, o projeto é excluído (com aviso em D-7 e D-1 e exportação disponível).
- Não oferecemos plano Free permanente no lançamento: cada projeto custa servidor + IPv4 público + IP residencial dedicado mesmo sem uso (seção 8). O caminho grátis permanente é o **self-host** (seção 2.9), como no Supabase.
- Avaliado e descartado para o lançamento: Free permanente com hibernação/parada do servidor quando ocioso. O socket do WhatsApp cai durante a parada e mensagens recebidas nesse intervalo só chegam quando reconectar. Pode servir para um Free de "só envio" num host compartilhado no futuro (D4).

### 2.8 BYOC e dedicado (Enterprise)

Inspirado no BYOC do ClickHouse (o data plane roda na conta de nuvem do cliente; o control plane continua com o fornecedor):

- **Dedicado** (C3): projetos do cliente numa **conta AWS separada, só dele, dentro da nossa AWS Organization**, com faturamento nosso. É o mesmo provisionamento com outra conta (o control plane assume um papel IAM na conta do cliente).
- **BYOC** (depois de C3): igual ao modelo do ClickHouse, que roda o data plane na conta AWS do cliente com um papel IAM de gerenciamento criado por CloudFormation/Terraform [fonte: clickhouse.com/docs/cloud/reference/byoc]. O cliente cria na conta dele um papel IAM com permissões limitadas (via um template que publicamos); o control plane assume esse papel e provisiona VPC, EC2 e EBS lá. Os dados e o tráfego ficam na conta do cliente; ele paga a infraestrutura à AWS e nos paga licença/suporte por instância. Como já estamos na AWS, o BYOC é o mesmo código do data plane com outra credencial. Requisito do motor: nenhum. Requisito do control plane: toda chamada à AWS passa pela interface `Provider` (seção 3.2), parametrizada por conta/papel.
- IP de saída no BYOC: o cliente pode usar o nosso pool (proxy pela internet) ou trazer o próprio.

### 2.9 Self-host grátis

O caminho open source continua de primeira classe, como no Supabase: `litezap serve` em qualquer servidor ou `docker run`, com o mesmo painel. A documentação do site (peça #6) compara "LiteZap Cloud × self-host" com honestidade: o que a nuvem adiciona é operação (upgrades, backups, monitoramento), IP residencial gerenciado e suporte. Não existe recurso do motor exclusivo da nuvem; o que é exclusivo é o control plane.

## 3. Arquitetura

```
                         ┌──────────────────── Control plane (fechado) — VPC litezap, sa-east-1 ─────────────┐
 navegador ──────────▶   │ painel da nuvem (contas, orgs, cobrança) + painel do motor embutido               │
 CLI / parceiros ────▶   │ API de gerenciamento (lzm_)                                                        │
 Stripe / Asaas ─────▶   │ webhooks de cobrança (fim do C1)                                                   │
                         │                                                                                    │
                         │  RDS PostgreSQL (sub-rede privada) ◀── workers: provisionamento, uso, IPs,        │
                         │  Prometheus/VictoriaMetrics (scrape privado)   upgrades, cobrança                  │
                         └──────────┬─────────────────────────────┬──────────────────────┬───────────────────┘
                                    │ AWS SDK for Go (EC2, SSM,   │ API admin do motor    │ scrape :9090
                                    │ IAM, KMS, Parameter Store)  │ (token de operador)   │ (rede da VPC)
                                    ▼                             ▼                       ▼
       ┌──────────── Data plane: mesma conta, VPC litezap, sub-redes públicas em 3 AZs ───────────────────────┐
       │  EC2 t4g (projeto lz-k3f9q2, SG próprio, papel IAM próprio)   EC2 t4g (projeto lz-8hd2mm)      ...     │
       │  ┌───────────────────────────────────────────┐               ┌───────────────────────────┐            │
       │  │ Caddy :443 (Let's Encrypt) ─▶ 127.0.0.1:8080│               │ Caddy ─▶ litezap          │            │
       │  │ container litezap serve (supervisor+workers)│               │ EBS gp3 /var/lib/litezap  │            │
       │  │ EBS gp3 de dados → /var/lib/litezap         │               │                           │            │
       │  │ SSM Agent, CloudWatch Agent; sem SSH        │               │                           │            │
       │  └──────────────┬────────────────────────────┘               └─────────────┬─────────────┘            │
       └─────────────────┼────────────────────────────────────────────────────────────┼────────────────────────┘
                         │ cada worker sai pelo seu proxy                              │
                         ▼                                                             ▼
                proxy residencial BR (IP A)                               proxy residencial BR (IP B) ──▶ WhatsApp
```

### 3.1 Control plane

Código fechado, repositório separado, em Go (mesma stack do motor; reaproveita o cliente da API do motor e usa o AWS SDK for Go v2). Componentes:

- **API de gerenciamento** (`/v1`): organizações, membros, projetos, instâncias, chaves, uso, faturas. Instâncias são criadas aqui e o control plane chama `POST /v1/instances` no motor do projeto, atribui o IP e aplica `proxy_url`.
- **Painel do cliente**: login, organizações, projetos, cobrança, uso, logs. Para o que acontece dentro de um projeto (instâncias, QR, conversas, webhooks, eventos), **reaproveita o painel embutido do motor** (`/app/`) aberto na URL do projeto com login de um clique emitido pelo control plane (item E7 da seção 9). Toda melhoria do painel open source chega à nuvem sem retrabalho.
- **Postgres** (só metadados: contas, organizações, projetos, instâncias espelhadas, IPs, assinaturas, eventos de cobrança, uso agregado, auditoria). Nenhuma mensagem de WhatsApp. Recomendação: RDS PostgreSQL em sub-rede privada da mesma VPC (decisão D6).
- **Workers assíncronos**: provisionamento, sincronização de estado e uso, atribuição e saúde de IPs, ondas de upgrade, cobrança e dunning, exclusão. Fila em Postgres (`SELECT … FOR UPDATE SKIP LOCKED`; River em Go). Toda tarefa é idempotente e retomável (cada passo grava o estado e os IDs AWS criados, com tags que permitem achar recursos órfãos).
- **Ferramenta interna `lzctl`** (C1): o "provisionamento manual" do beta é um comando da equipe (`lzctl project create --org … --size …`) que enfileira a mesma tarefa que o self-service usará no C2. Manual é a aprovação, não os cliques no console da AWS.
- **Hospedagem do control plane**: no C1, uma EC2 `t4g.small` na mesma VPC (Caddy + binário do control plane via Docker/systemd), com o RDS em sub-rede privada; no C3, duas instâncias atrás de um ALB (quando houver domínio próprio para o ACM, seção 3.7) ou ECS Fargate (o control plane não tem estado local, então Fargate serve aqui).

### 3.2 Interface de provedor

Todo acesso à infraestrutura passa por uma interface do control plane, para permitir conta dedicada, BYOC e o empacotamento do C2:

```
Provider.CreateProject(ref, size, image, secrets) → ProjectInfra
Provider.UpdateImage(ref, image) / Resize(ref, size) / Stop / Start / Destroy
Provider.Snapshot(ref) / Restore(ref, snapshotID, az)
Provider.RunCommand(ref, document, params) → resultado
Provider.Status(ref) → {estado da EC2, status checks, uso do volume}
```

Implementações: `EC2Provider` (C1: uma EC2 por projeto) e `HostProvider` (C2/C3: container num host compartilhado, seção 3.12). Ambas parametrizadas por conta AWS/papel IAM (para dedicado e BYOC).

### 3.3 Data plane no C1: uma EC2 Graviton por projeto

**Base (criada uma vez pelo Terraform, seção 3.11)**: VPC em `sa-east-1` com sub-redes públicas nas 3 AZs e privadas para o RDS; sem NAT Gateway (custo fixo alto; as EC2 dos projetos têm IPv4 público de qualquer forma); repositório ECR com a imagem do motor replicada do GHCR; chave KMS; política DLM; documentos SSM próprios; *launch template* apontando para a AMI.

**AMI** (construída com Packer ou EC2 Image Builder, mensal e a cada correção de segurança): Amazon Linux 2023 arm64 com Docker, Caddy, SSM Agent (já vem no AL2023), CloudWatch Agent e as units systemd do LiteZap. Sem chave SSH e sem porta 22.

**Por projeto (criado pelo control plane via AWS SDK for Go)**, cada passo idempotente e marcado com as tags `litezap:project=<ref>` e `litezap:managed=true`:

1. **Parâmetros** no Parameter Store, sob `/litezap/projects/<ref>/` (SecureString com a chave KMS): `admin_token`, `operator_token`, `public_url`, `hostname`, versão da imagem, tamanho.
2. **Papel IAM do projeto** (`/litezap/projects/<ref>-role`, com *permissions boundary*): só `ssm:GetParameter*` no caminho do projeto, `kms:Decrypt` condicionado a esse caminho, `logs:PutLogEvents` no log group do projeto, leitura do ECR e a política mínima do SSM Agent. Nenhuma permissão de EC2/IAM.
3. **Security group do projeto**: entrada TCP 443 de `0.0.0.0/0` (e 80 só se usarmos o desafio HTTP-01, seção 3.7) e TCP 9090 (ops) só a partir do SG do monitoramento; saída liberada (proxy, webhooks, download de mídia). Um SG por projeto: projetos não alcançam portas uns dos outros.
4. **Volume EBS gp3 de dados** (tamanho pelo plano, a partir de 4 GB, XFS) separado do volume raiz, com a tag de backup do plano (seção 3.8). Volume separado permite trocar a raiz (AMI nova) ou mover os dados para outra instância sem tocar no projeto.
5. **EC2** a partir do *launch template* (tipo `t4g.*` pelo tamanho, modo de crédito `unlimited`, IMDSv2 obrigatório, perfil de instância do passo 2), na AZ do volume; anexa o volume.
6. **IPv4 elástico** associado (o hostname temporário contém o IP, seção 3.7; o IP precisa sobreviver a stop/start).
7. **Boot**: uma unit `litezap-config` lê os parâmetros e grava `/etc/litezap/env` (0600); a unit `litezap` roda `docker run --restart unless-stopped` com a imagem do ECR, `LITEZAP_LISTEN_ADDR=127.0.0.1:8080`, `LITEZAP_DATA_DIR=/data` (o volume EBS), `LITEZAP_WORKER_MEMORY_MAX_MB=256`, limite de memória do container e driver de log `awslogs`; o Caddy obtém o certificado e encaminha para `127.0.0.1:8080`.
8. O control plane espera `https://<hostname>/readyz`, cadastra o webhook de operador de `instance.status`, cria a primeira instância se pedida e marca o projeto `active`.

Gerência sem SSH: comandos operacionais (upgrade, diagnóstico, exportação) vão por **SSM Run Command** com documentos próprios e versionados; acesso interativo de emergência só por **Session Manager**, com sessão registrada no CloudWatch Logs/S3 e restrita a um grupo de operadores com MFA.

### 3.4 Sempre ligado e recuperação

- Servidor sempre ligado em todo projeto pago. Dentro dele: Docker reinicia o container (`unless-stopped`), systemd reinicia Docker/Caddy, e o supervisor reinicia workers com backoff e circuit breaker (seção 9.2 do motor).
- **Falha de host**: o EBS é armazenamento de rede da AZ, não disco local. A *recuperação automática* do EC2 move a instância para outro host com o mesmo volume, IP e metadados quando a verificação de status do sistema falha (habilitada por padrão nos tipos suportados, que incluem `t4g`; confirmar no C1 (incerto)) [fonte: docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-recover.html]. RPO ≈ 0 para falha de host: melhor que um volume local.
- **Falha de AZ**: o volume fica preso à AZ. Recuperação = criar volume a partir do último snapshot em outra AZ e uma EC2 nova (RPO até 24 h no C1; reduzido pelo Litestream no C3). As AZs são espalhadas entre projetos para que uma falha de AZ atinja ~1/3 da base.
- Um único servidor por projeto, sem réplica: duas instâncias whatsmeow com a mesma sessão brigariam pela conexão. Isso limita o SLA possível (seção 8.1).

### 3.5 Health checks

- **No servidor**: `HEALTHCHECK` do container chamando `/healthz` e alarme do CloudWatch nas verificações de status da EC2 (recuperação automática).
- **Endpoint de ops sem token**: hoje o motor exige token admin em `/healthz` e `/readyz` quando o bind não é loopback. Com o bind da API em `127.0.0.1:8080` o check local já funciona sem token, mas o scrape de métricas pela VPC precisa de outro endereço. Mudança E2: `ops_listen_addr` (ex.: `0.0.0.0:9090`, protegido pelo SG) com `/healthz`, `/readyz` e `/metrics` sem token.
- **Sonda externa** do control plane a cada 60 s: `GET https://<hostname>/readyz` + `GET /v1/instances` (operador). Alimenta a página de estado do projeto e os alertas (seção 8.2).
- **Saúde por instância**: `instance.status` chega ao control plane por um webhook global do operador cadastrado no provisionamento, com estado e motivo (`proxy_unreachable`, `logged_out`, `banned`, `temp_banned:`).

### 3.6 Upgrades em ondas e rollback

- A imagem do motor é versionada por tag semver no ECR; cada projeto guarda `engine_version` e `release_channel` (`stable`, `canary`) e a tag `litezap:wave` (0 = interno, 1 = canário, 2, 3).
- **Ondas**: (0) projetos internos → (1) canário: ~5% dos projetos pagos (opt-in ou sorteio entre os menores) → (2) 25% → (3) 100%. Cada onda espera 24 h (canário) ou 2–6 h e só avança se os indicadores ficarem no limite: reconexão em 5 min ≥ 99%, sem alta de `logged_out`/`banned`, crash de worker e 5xx estáveis.
- **Mecânica**: o control plane cria um snapshot do volume de dados (`CreateSnapshot`) e roda o documento SSM `LiteZap-Upgrade` (parâmetro `version`) com alvo pela tag da onda, usando o controle de taxa do Run Command (`MaxConcurrency` 10%, `MaxErrors` 1, que para a onda no primeiro erro) [fonte: docs.aws.amazon.com/systems-manager/latest/userguide/send-commands-velocity.html]. O documento faz `docker pull`, troca a tag no env, `docker stop -t 30` (SIGTERM → shutdown gracioso do motor) e sobe o container novo; espera `/readyz` local e devolve o resultado. Indisponibilidade esperada por projeto: 10–30 s (estimativa).
- **Janela**: 02:00–05:00 BRT para upgrades não urgentes; correções de segurança podem sair fora da janela. Team+ pode adiar até 14 dias (como as janelas de manutenção do ClickHouse).
- **Rollback**: mesmo documento com a versão anterior. Exige migrações do SQLite compatíveis com uma versão para trás (regra de processo do motor: migração destrutiva só duas versões depois). Se não der, restaurar o snapshot pré-upgrade (último recurso).
- **Sistema operacional**: SSM Patch Manager com janela de manutenção semanal para patches do AL2023 (reboot dentro da janela); trocas de AMI com "replace root volume", que mantém o volume de dados, IP e ID da instância.

### 3.7 Rede, ingresso e domínio temporário

- **Saída**: tudo que o worker faz com o WhatsApp passa pelo proxy residencial da instância (seção 4.6 do motor; seção 5). O supervisor sai direto pelo IPv4 público da EC2 para baixar `media.url` e entregar webhooks (nunca fala com o WhatsApp).
- **Entrada**: só HTTPS 443 no IPv4 elástico do projeto. API, painel e MCP HTTP (`/mcp`) na mesma URL; `public_url` = essa URL, para as URLs assinadas de mídia.

**TLS: Caddy na instância × ALB + ACM**

| | Caddy na EC2 + Let's Encrypt | ALB + ACM |
|---|---|---|
| Domínio | funciona com domínio temporário tipo sslip.io | o ACM só emite certificado para nomes cuja posse provamos (DNS, e-mail ou HTTP); não serve para sslip.io, nip.io nem `amazonaws.com` [fonte: docs.aws.amazon.com/acm/latest/userguide/domain-ownership-validation.html] |
| Custo | zero | US$ 0,034/h (~US$ 24,82/mês) por ALB + US$ 0,011 por LCU-hora em `sa-east-1`, mais os IPv4 públicos do ALB; por projeto é inviável, um ALB compartilhado só com domínio próprio e roteamento por host |
| Operação | renovação automática na própria instância; um ponto a mais para manter na AMI | gerenciado; precisa de alvos e regras por projeto |
| Corpo grande (mídia até 140 MB) | sem limite extra | sem problema |

Recomendação (D11): **Caddy na instância no C1 e C2**. ALB compartilhado + ACM com curinga só depois de termos domínio próprio e hosts compartilhados (C3), e mesmo assim só se trouxer algo além de TLS (WAF, por exemplo).

**Domínio temporário** — opções avaliadas:

| Opção | Como fica | Prós | Contras |
|---|---|---|---|
| **sslip.io** (e nip.io, hoje o mesmo serviço) | `lz-k3f9q2.54-232-10-7.sslip.io` (o DNS do sslip.io devolve o IP escrito no nome) | zero custo, nada para configurar, funciona com Let's Encrypt (HTTP-01/TLS-ALPN-01 por hostname; sem curinga); serviço com mais de 10 anos e >20 mil consultas/s, e o Let's Encrypt elevou a cota do domínio para 250 mil certificados/semana [fonte: nip.io] | **o DNS dos clientes passa a depender de um serviço gratuito de terceiros sem SLA** (se ele cair, as URLs param de resolver); o nome expõe o IP; o IP precisa ser fixo (IPv4 elástico) |
| Certificado Let's Encrypt para o próprio IP | `https://54.232.10.7` | zero custo, sem DNS de terceiros; o Let's Encrypt emite certificados para IP desde 2026-01-15, com validade de ~6 dias (perfil *shortlived*) [fonte: letsencrypt.org/2026/01/15/6day-and-ip-general-availability] | URL feia; renovação a cada poucos dias (automatizada, mas é mais um ponto de falha); suporte a perfis ACME na versão do Caddy a confirmar (incerto) |
| DNS público da EC2 (`ec2-….compute.amazonaws.com`) | gerado pela AWS | zero custo | o Let's Encrypt recusa nomes `amazonaws.com` ("Policy forbids issuing for name", relatos no fórum; a lista de bloqueio não é publicada) (incerto) [fonte: community.letsencrypt.org/t/policy-forbids-issuing-for-name-on-amazon-ec2-domain/12692]; muda se o IP mudar |
| Domínio barato qualquer (ex.: `.com.br` no Registro.br, ~R$ 40/ano) + Route 53 | `lz-k3f9q2.algumnome.com.br` | nomes estáveis, independentes do IP; habilita ACM/ALB; Route 53 ~US$ 0,50/mês por zona | não é custo zero; o nome definitivo do produto ainda não existe |

Recomendação: **sslip.io no C1** (com o certificado por IP como plano B se o sslip.io ficar fora do ar), com o hostname tratado como dado do projeto (`projects.hostname` no Postgres e no Parameter Store), nunca deduzido do IP em outro lugar. Para trocar depois: o control plane adiciona o hostname novo ao Caddy (os dois funcionam ao mesmo tempo), atualiza `public_url`, avisa o cliente e mantém o antigo por 90 dias. Como clientes gravam a URL em integrações e webhooks de entrada, a troca deve acontecer **antes do lançamento público do C2**: registrar um domínio (mesmo provisório, ~R$ 40/ano) é a recomendação para o fim do C1.

Limites do Let's Encrypt: 50 certificados por domínio registrado por semana e 5 por conjunto idêntico de nomes por semana; renovações via ARI ficam fora [fonte: letsencrypt.org/docs/rate-limits]. O sslip.io não está na Public Suffix List, mas tem cota própria elevada (250 mil/semana), então não é gargalo.

### 3.8 Backups e snapshots

- **Snapshots EBS pelo Data Lifecycle Manager (DLM)**: políticas por tag (`litezap:backup=daily-7`, `daily-30`) criadas no Terraform; snapshots diários incrementais, retidos 7 (Pro) ou 30 dias (Team). O DLM não cobra à parte; paga-se o armazenamento dos snapshots [fonte: aws.amazon.com/ebs/pricing]. Snapshots ficam em `sa-east-1` (sem cópia para outra região, por LGPD e custo; cópia opcional no Enterprise).
- **Snapshot sob demanda** antes de cada upgrade e antes de exclusões.
- **Exportação lógica** (E8): endpoint do motor que gera um arquivo com `litezap.db` (backup online do SQLite), mídia e configurações para uma URL S3 pré-assinada (bucket em `sa-east-1`, criptografado com KMS, ciclo de vida de 30 dias). Team: exportação agendada semanal para um bucket do próprio cliente.
- **Cuidado com sessões**: o *store* do whatsmeow guarda chaves e estados de *ratchet* do Signal. Restaurar uma cópia antiga pode gerar falhas de decifragem ou exigir novo pareamento (incerto quanto à frequência). A restauração padrão recupera log de mensagens, webhooks e configurações; o cliente reescaneia o QR se a sessão não voltar.
- **Equivalente a PITR**: Litestream do `litezap.db` para S3 (C3, Team+). Não se aplica às sessões.

### 3.9 Logs e métricas

- **Logs**: o container usa o driver `awslogs` (log group `/litezap/projects/<ref>`, retenção pelo plano). O motor já usa `slog` com `instance_id`; mudança E9: JSON em stdout (`LITEZAP_LOG_FORMAT=json`) sem telefones. O explorador de logs do painel (C2) consulta o CloudWatch Logs Insights com filtro obrigatório pelo log group do projeto. Custo por GB ingerido e armazenado na seção 8.4; se o volume crescer, exportar para S3 e consultar com Athena, ou trocar para Grafana Loki/ClickHouse (D9).
- **Métricas**: as métricas básicas da EC2 (CPU, rede, status checks) vêm de graça no CloudWatch; memória e disco pelo CloudWatch Agent (poucas métricas customizadas por projeto). As métricas da aplicação (`/metrics`, E3) **não** vão para o CloudWatch como métricas customizadas (cobradas por métrica por mês, caro com dezenas de séries × projetos): um Prometheus (ou VictoriaMetrics) no control plane faz *scrape* do endpoint de ops de cada projeto pela rede da VPC, com descoberta pelas tags EC2 (`ec2_sd_config`). Grafana para a equipe; gráficos por projeto no painel do cliente (C2) via PromQL com filtro pelo projeto. Alternativa gerenciada: Amazon Managed Service for Prometheus (cobrança por amostra; avaliar no C3).
- **Uso para cobrança** não depende de métricas: vem de contadores persistidos no motor (E11), lidos pelo worker de uso a cada hora.

### 3.10 Segredos

- Gerados no control plane e guardados em dois lugares, ambos cifrados com a chave KMS: **Parameter Store SecureString** em `/litezap/projects/<ref>/` (de onde a EC2 lê no boot, com o papel IAM restrito ao próprio caminho) e no Postgres do control plane (cifrado por envelope com a mesma chave KMS). O nível padrão do Parameter Store não cobra por parâmetro; a KMS cobra pela chave e por requisição (seção 8.4).
- O env com segredos fica em `/etc/litezap/env` (0600, root), lido pelo Docker; nunca na *user data* (visível na API da EC2), nunca em parâmetros de Run Command (ficam no histórico do SSM).
- Credenciais do proxy de cada instância: guardadas cifradas no control plane e aplicadas no motor via `PUT /settings` com o token de operador; o motor já esconde a senha no `GET /settings`.
- Token de operador: um por projeto, nunca exposto ao cliente, rotacionado a cada 90 dias e em toda suspeita (o motor aceita dois tokens durante a rotação; E4).
- Pessoas: acesso à conta AWS só por IAM Identity Center com MFA; papel de "break-glass" separado e alertado.

### 3.11 Infra como código

Duas camadas com ferramentas diferentes:

1. **Base, estável, poucas mudanças** (conta, VPC, sub-redes, SGs base, RDS, chave KMS, ECR, papéis e políticas do control plane, *permissions boundary* dos papéis de projeto, políticas DLM, documentos SSM, *launch template*, buckets S3, log groups base, orçamentos e alarmes de conta): **Terraform** (compatível com OpenTofu), estado em S3 com travamento nativo do S3, um diretório por ambiente (`staging`, `prod`). A AMI é construída com **Packer** (mesmo ecossistema HashiCorp) e o ID publicado num parâmetro SSM lido pelo *launch template*.
2. **Recursos por projeto** (papel IAM, SG, volume, EC2, IPv4 elástico, parâmetros, tags): criados pelo **control plane com o AWS SDK for Go v2**, não pelo Terraform. Um estado Terraform por cliente não escala (centenas de *applies*, estados para conciliar) e o fluxo de provisionamento precisa ser uma tarefa transacional com retentativas, que o control plane já sabe fazer. Recursos por projeto sempre levam as tags `litezap:managed=true` e `litezap:project=<ref>`; um job diário de conciliação compara AWS × Postgres e alerta sobre órfãos.

Por que Terraform e não AWS CDK: o CDK tem suporte a Go, mas é um *wrapper* sobre CloudFormation com o runtime do jsii (Node.js) por baixo, e a comunidade e os exemplos são majoritariamente TypeScript; o Terraform é declarativo, independente de linguagem, mais conhecido por quem vai operar e cobre também recursos fora da AWS que vamos usar (DNS quando houver domínio, Grafana, provedor de logs, GitHub). Também permite que o template de BYOC (papel IAM na conta do cliente) seja publicado em Terraform, como faz o ClickHouse (que oferece CloudFormation e Terraform). CDK seria preferível só se o time já fosse TypeScript-first.

### 3.12 Empacotamento no C2/C3 (vários projetos por servidor)

Uma EC2 por projeto é simples e isola bem, mas cada projeto paga um mínimo fixo (instância + IPv4 público + overhead de SO/Docker/Caddy). Para projetos pequenos (teste, 1–3 instâncias) isso pesa (seção 8.4). Opções avaliadas:

| Opção | Como | Avaliação |
|---|---|---|
| **(a) Hosts compartilhados com um container e um volume EBS por projeto** | EC2 maiores (`m7g`/`r7g` ou `t4g.xlarge`) rodando N containers `litezap`, cada um com seu volume EBS anexado (Nitro permite ~27 volumes por instância, incerto por tipo) ou um diretório com cota XFS num volume maior; um Caddy por host roteando por hostname; agendamento feito pelo control plane (`HostProvider`) | **Recomendado para o C2** em projetos de teste e Pro pequenos. Reaproveita AMI, SSM e Caddy. Snapshot por volume continua por projeto. Isolamento passa de VM para container (namespaces, cgroups, usuário não root, seccomp, sem Docker socket): aceitável para teste/Pro pequeno; Team e Enterprise continuam com EC2 dedicada. Um IPv4 por host em vez de um por projeto |
| (b) ECS em EC2 com EBS | o ECS gerencia tarefas; volume EBS por tarefa | O EBS integrado ao ECS é criado no início da tarefa e, para tarefas de um *service*, **sempre apagado** quando a tarefa termina (só pode ser semeado de snapshot) [fonte: docs.aws.amazon.com/AmazonECS/latest/developerguide/configure-ebs-volume.html]: perderíamos o SQLite a cada troca. Daria para usar *bind mount* com restrição de posicionamento no host, mas aí o ECS só acrescenta complexidade ao que (a) já faz |
| (c) Fargate + EFS | tarefas sem servidor com sistema de arquivos de rede | **Descartado**: o SQLite em sistema de arquivos de rede tem travamento não confiável (a FAQ do SQLite desaconselha NFS) e o modo WAL, usado pelo motor e pelo store do whatsmeow, exige memória compartilhada no mesmo host e não funciona em sistema de arquivos de rede [fontes: sqlite.org/faq.html#q5; sqlite.org/wal.html]. Além disso, latência de EFS em cada escrita e Fargate mais caro por GB de memória que EC2 |
| (d) Kubernetes (EKS) | um pod com PVC EBS por projeto | Funciona tecnicamente (StatefulSet + EBS CSI), mas o custo fixo do plano de controle do EKS e a operação de um cluster não se pagam no nosso tamanho. Reavaliar só no Enterprise/BYOC grande |

Recomendação: **(a) no C2 para o teste grátis e Pro com até 3 instâncias**, EC2 dedicada para o resto; revisar no C3 com dados reais de memória por instância.

## 4. Fluxo da instância na nuvem

1. Cliente cria instância (painel, CLI ou API de gerenciamento). O control plane verifica plano, teto e cota; reserva um IP do pool (seção 5) compatível com a UF escolhida.
2. Chama `POST /v1/instances` no motor (token de operador) → recebe `{instance, token}`; mostra o token ao cliente uma vez (o control plane não guarda o token de instância, só o id).
3. `PUT /v1/instances/{id}/settings {"proxy_url": "socks5://…"}` com o token de operador (campo travado para o cliente). Trocar o proxy reinicia o worker.
4. Cliente pareia (QR ou código) no painel. Ao receber `instance.status: connected`, o control plane marca a instância como **ativa para cobrança** e registra `ip_id ↔ instance_id ↔ phone_hash` (para a regra "um cliente por IP").
5. Exclusão: `DELETE /v1/instances/{id}` no motor; o IP entra em **quarentena** (seção 5.5) antes de voltar ao pool.

## 5. Estratégia de IP de saída

### 5.1 Por que não usar o IP da AWS direto

- **IP de datacenter**: os IPs públicos da EC2 pertencem aos ASNs da Amazon e a própria AWS publica todas as suas faixas em `ip-ranges.json` [fonte: docs.aws.amazon.com/vpc/latest/userguide/aws-ip-ranges.html], então qualquer serviço classifica um IP da AWS como datacenter sem esforço. Não há declaração da Meta, mas há relatos consistentes da comunidade de clientes não oficiais: um projeto baseado em Baileys descreve restrição silenciosa de aparelhos vinculados que conectam de IPs de VPS (o envio "funciona" e a mensagem não chega) [fonte: github.com/arslanstack/whatsapp-baileys]; um tópico de suporte da Railway relata conexão recusada a partir de datacenter e funcionando de casa [fonte: station.railway.com/questions/railway-blocks-websockets-9ac08fce]; a Whapi.Cloud, concorrente, afirma que o WhatsApp compara a sessão com a região do número [fonte: support.whapi.cloud/help-desk/blocking/using-residential-proxies-with-whapi.cloud]. Tudo **anedótico**; na discussão do whatsmeow há quem defenda que banimento vem do comportamento, não da biblioteca [fonte: github.com/tulir/whatsmeow/discussions/979].
- **Contaminação**: um IP de datacenter compartilhado entre clientes marca todos quando um deles faz spam (risco listado no spec do motor).
- **Granularidade**: o IPv4 elástico é da instância EC2, ou seja, do projeto inteiro; um IP por instância de WhatsApp exigiria várias interfaces de rede e roteamento por processo, com o mesmo problema de reputação. Todo IPv4 público custa US$ 0,005/h (~US$ 3,65/mês) desde 2024 (seção 8.4).

Conclusão: o IPv4 elástico do projeto fica como **plano de contingência** (projeto inteiro saindo por um IP fixo de datacenter quando o provedor de proxy cai e o cliente prefere isso a ficar desconectado; opt-in, com aviso) e como IP fixo para o **supervisor**: como o IPv4 elástico do projeto não muda, o cliente pode liberá-lo no firewall do receptor de webhooks sem custo extra (no Fly isso era um add-on). Nunca como padrão para os sockets do WhatsApp.

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

> **Momento**: o beta do C1 começa com cobrança manual (Pix/boleto avulso, preço combinado por cliente). Tudo desta seção (gateways, slots, proração, dunning automático, webhooks) é o **último passo do C1**, antes do self-service do C2 (seção 10). O uso diário (E11) é registrado desde o primeiro dia para que a cobrança automática comece com histórico.

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
| D+14 | **projeto pausado** | EC2 parada (fica o custo do EBS e do IPv4 elástico); sessões ficam no volume; painel só mostra cobrança e exportação (E21 permite subir o motor só para exportar) |
| D+30 | aguardando exclusão | aviso final; exportação continua disponível |
| D+45 | excluído | EC2, volume, snapshots, IPv4 elástico, papel IAM e parâmetros destruídos; IPs para quarentena; registros fiscais mantidos |

Pagamento em qualquer momento antes de D+45 reativa tudo automaticamente (envio liberado na hora; projeto pausado volta em ~1 min, e as sessões provavelmente reconectam sem QR se a pausa foi curta (incerto, seção 2.7)). Prazos configuráveis por plano; Enterprise por contrato.

### 6.5 Webhooks de cobrança (idempotência)

- Endpoint por provedor; verificação de assinatura (Stripe: `Stripe-Signature`; Asaas: token de acesso configurado no webhook) antes de qualquer coisa.
- **Gravar e responder**: insere em `billing_events` com `UNIQUE(provider, provider_event_id)`; duplicata = responde 200 sem fazer nada. Responde 200 rápido e processa de forma assíncrona (o Asaas entrega pelo menos uma vez, pausa a fila depois de 15 falhas seguidas e guarda eventos por 14 dias; só o modo "sequencial" garante ordem [fontes: docs.asaas.com/docs/receba-eventos-do-asaas-no-seu-endpoint-de-webhook; docs.asaas.com/docs/fila-pausada]).
- **Ordem não confiável**: o processador nunca aplica o estado do evento às cegas; busca o objeto atual no provedor (assinatura, cobrança) e reconcilia. Transições de estado da assinatura no control plane são monotônicas por `period_end` e versão.
- **Efeitos colaterais idempotentes**: ações no data plane (liberar envio, iniciar a EC2) são tarefas com chave `(projeto, ação, período)`.
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
| Computação | uma EC2 (VM Nitro) por projeto no C1; nenhum processo de dois clientes na mesma VM. A partir do C2, projetos pequenos podem dividir um host em containers isolados (seção 3.12); Team/Enterprise continuam com VM dedicada |
| Rede | security group por projeto: só 443 aberto ao mundo e a porta de ops só para o monitoramento; projetos não alcançam portas uns dos outros; sem SSH |
| Dados | volume por projeto; nenhuma mensagem no Postgres do control plane |
| Segredos | caminho próprio no Parameter Store e papel IAM próprio por projeto (lê só o seu caminho); token de operador e admin por projeto |
| IP de saída | um IP por instância, nunca reatribuído a outro cliente antes da quarentena (5.5) |
| Painel | o painel do motor de um projeto só aceita o token daquele projeto; o login de um clique é emitido pelo control plane depois de checar o papel do membro |

Permissões do control plane na AWS: um papel com o mínimo necessário, condicionado por tags (só termina/altera recursos com `litezap:managed=true`; `iam:CreateRole` só no caminho `/litezap/projects/` e com *permissions boundary* obrigatório; `iam:PassRole` só desses papéis; `ssm:SendCommand` só com os nossos documentos). Os papéis dos projetos não têm nenhuma permissão de EC2/IAM. CloudTrail ligado na conta (trilha para S3 com retenção de 1 ano) e GuardDuty para detectar uso anômalo. No C3, o data plane vai para uma conta AWS separada da do control plane (AWS Organizations), limitando o raio de um incidente. O control plane nunca roda código do cliente.

### 7.2 LGPD

- **Papéis**: o cliente é o controlador dos dados das conversas; o LiteZap é operador. Contrato de processamento de dados (DPA) anexo aos termos.
- **Localização**: EC2, volumes EBS, snapshots, logs do CloudWatch, Parameter Store e Postgres do control plane, todos em `sa-east-1` (São Paulo); nenhuma cópia para fora do Brasil por padrão (logs podem conter JIDs; se um destino de logs fora da AWS for adotado, D9, mascarar telefones antes).
- **Minimização**: o motor já evita telefones e tokens nas mensagens de erro. O control plane guarda só o hash do telefone (para a regra de IP e para detectar abuso), não o número em claro, salvo o que o cliente informar no cadastro.
- **Retenção**: log de mensagens conforme `message_retention_days` escolhido pelo cliente (padrão da nuvem: 90 dias, configurável até ilimitado no Team); mídia 7 dias (`media_retention_days`; limpeza automática é pré-requisito, item E15); logs de aplicação conforme o plano; snapshots conforme o plano.
- **Direitos do titular**: o cliente atende os titulares usando a API (apagar chat/mensagens — endpoint novo E16 — e exportar). O LiteZap atende pedidos do próprio cliente (dados de conta e cobrança).
- **Exportação e exclusão do projeto**: exportação a qualquer momento (E8). Excluir um projeto destrói EC2, volume e snapshots em até 7 dias (fila de exclusão com período de arrependimento de 7 dias, desligável); o control plane mantém só os registros fiscais e de cobrança pelo prazo legal.
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
- Plantão com escalonamento (seção 8.2); comunicação na página de status pública (página de status hospedada; domínio próprio quando houver, D11) com componentes: API de gerenciamento, painel, projetos (data plane), pool de IPs, cobrança.
- Vazamento ou suspeita: rotacionar tokens de operador e admin dos projetos afetados (o motor aceita token duplo durante a rotação), revogar chaves `lzm_`, rotacionar credenciais do provedor de proxy, notificar os clientes sem demora. Para os dados das conversas o cliente é o controlador e é dele a obrigação de comunicar a ANPD e os titulares (Resolução CD/ANPD nº 15/2024: 3 dias úteis a partir de saber que o incidente envolve dados pessoais; o dobro para agentes de pequeno porte); como operador, o LiteZap entrega ao cliente as informações para isso. Para os dados de conta e cobrança o LiteZap é controlador e comunica ele mesmo [fontes: legisweb.com.br/legislacao/?id=458235; mattosfilho.com.br/unico/regulamento-incidente-seguranca].
- Pós-incidente escrito, sem culpados, publicado para sev1.

### 7.5 Outros controles

- SSO/SAML e logs de auditoria (quem criou/excluiu projeto, viu o admin token, mudou plano) para Team+; o log de auditoria existe desde o C1 internamente, só a exibição é por plano.
- 2FA (TOTP) para todos os membros, obrigatório para Owner/Admin.
- Allowlist de IP por token (M11 do doc de paridade; E10) para clientes que pedem.
- Revisão de segurança do motor (o painel e a API ficam expostos à internet na nuvem, diferente do uso local) antes do C1: CSP, limites de corpo, `trusted_proxies` (o Caddy na frente do motor envia `X-Forwarded-For`).

## 8. Operação

### 8.1 SLOs

| Indicador | Objetivo | Medição |
|---|---|---|
| Disponibilidade da API de gerenciamento e do painel | 99,5% mensal (C1–C2), 99,9% (C3) | sonda externa a cada 1 min |
| Disponibilidade do projeto (data plane) | 99,5% mensal Pro; 99,9% Team a partir do C3 | `GET https://<hostname>/readyz` a cada 1 min, de fora da AWS |
| Reconexão depois de restart/upgrade | 99% das instâncias que estavam `connected` voltam a `connected` em ≤ 2 min | eventos `instance.status` |
| Reconexão depois de falha de proxy | ≤ 15 min até a troca automática de IP (C2) | idem + log de trocas |
| Provisionamento de projeto | C1: < 5 min (manual); C2: p95 < 90 s; C3 (hosts compartilhados): p95 < 60 s | tarefas do control plane |
| Criação de instância até QR | p95 < 15 s | idem |
| Latência de webhook (evento → primeira tentativa) | p95 < 5 s | métricas do motor (E3) |

Fora do SLA: banimento, logout pelo celular, celular principal inativo, indisponibilidade do WhatsApp, manutenção programada avisada com 48 h. 99,5% permite ~3,6 h fora por mês; na AWS a falha de host é coberta pela recuperação automática da EC2 com o mesmo volume EBS (minutos, sem perda de dados); o risco dominante passa a ser a falha de uma AZ (recuperação por snapshot em outra AZ, RPO até 24 h, alvo de 30 min) e erros nossos em upgrades. Por isso 99,9% só é prometido depois de C3 (Litestream, runbook de AZ testado). SLA com crédito como Supabase e ClickHouse (créditos de 10–30% da mensalidade conforme a faixa, limitados) [fontes: supabase.com/sla; clickhouse.com/legal/agreements/service-level-agreement].

### 8.2 Plantão e alertas

- C1: rodízio entre os fundadores, horário estendido (08–22h BRT) para sev2 e 24×7 para sev1; alarmes do CloudWatch e do Grafana (Alertmanager) → SNS → e-mail/SMS/app de plantão (Grafana OnCall ou PagerDuty). C3: plantão formal com no mínimo 3 pessoas.
- Regras sobre métricas da aplicação rodam no Prometheus do control plane; regras sobre infraestrutura (status checks, CPU com crédito esgotado, disco) em alarmes do CloudWatch; regras de negócio (cobrança, pool de IPs) em jobs do control plane.
- **AWS Budgets** com alertas de custo da conta (diário e mensal) e detecção de anomalias de custo, para pegar recursos órfãos ou abuso.

| Alerta | Severidade |
|---|---|
| projeto pago com `/readyz` falhando por 3 min | sev2 (sev1 se ≥ 5% dos projetos) |
| % de instâncias em `reconnecting` no pool > 5% por 5 min | sev1 |
| pico de `banned`/`temp_banned` (2× a média de 30 dias) | sev2 |
| taxa de erro do provedor de proxy > 2% nas sondas | sev2 |
| falhas nas APIs da AWS / provisionamento > 3 seguidas (inclui `InsufficientInstanceCapacity` e erros de cota) | sev2 |
| fila do Asaas pausada, webhook do Stripe falhando, conciliação com diferença | sev2 |
| volume > 80%, memória da EC2 > 85% por 15 min, saldo de créditos de CPU em queda contínua, restarts de worker em alta | sev3 (aciona redimensionamento) |
| snapshot diário ausente, certificado a 14 dias de expirar, estoque de IPs livres < 10% | sev3 |
| uso de qualquer cota da conta (vCPUs on-demand, IPv4 elásticos, papéis IAM, volumes) > 80% | sev3 |

### 8.3 Capacidade

- **Cotas da conta AWS** (verificar os valores atuais em Service Quotas e pedir aumento **antes do beta**; aumentos podem levar dias):
  - **vCPUs de instâncias On-Demand padrão** (famílias A, C, D, H, I, M, R, T, Z; código `L-1216C47A`): **5 vCPUs por padrão** em contas novas, aumentado automaticamente com o uso ou por pedido [fonte: docs.aws.amazon.com/ec2/latest/instancetypes/ec2-instance-quotas.html]. Cada `t4g` tem pelo menos 2 vCPUs: com 5 vCPUs cabem **2 projetos**. Conferir o valor da nossa conta e pedir ~100 vCPUs para o beta (50 projetos + control plane + folga).
  - **IPv4 elásticos por região**: 5 por padrão [fonte: docs.aws.amazon.com/vpc/latest/userguide/amazon-vpc-limits.html]. Cada projeto do C1 usa um; pedir aumento para o tamanho do beta mais folga (ex.: 50, depois 200).
  - **Papéis IAM por conta**: 1.000 por padrão (ajustável) [fonte: docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html]; um por projeto no C1.
  - **Security groups por VPC** e **regras por SG**, **armazenamento EBS gp3 por região** e **snapshots por região**: monitorar; todos ajustáveis.
  - Taxa das APIs da EC2 (*throttling* por conta): o worker de provisionamento usa limitador e retentativa com backoff.
- **Memória**: a tabela 2.6 é ponto de partida; medir RSS de supervisor, workers, Docker, Caddy e agentes no C1 (com e sem *history sync*) e ajustar. Redimensionar quando a memória passar de 85% por 15 min (trocar o tipo da EC2 exige parar e iniciar: ~1–2 min de indisponibilidade, feito na janela).
- **CPU**: `t4g` é burstável. Em modo `unlimited` não há estrangulamento, mas uso sustentado acima da linha de base é cobrado por vCPU-hora (seção 8.4); alarme quando um projeto ficar acima da linha de base por dias (sinal de subir para `m7g`/`m8g`).
- **Volume**: começa em 4 GB por projeto, extensão automática online pelo control plane (`ModifyVolume` + `xfs_growfs` via SSM). A limpeza de mídia (E15) é o que mantém o volume pequeno.
- **IPs residenciais**: compra antecipada mantendo 10–20% livres; prazo de entrega do fornecedor medido no C1.
- **Capacidade de `sa-east-1`**: falta de capacidade de um tipo numa AZ (`InsufficientInstanceCapacity`) é rara mas possível; o provisionamento escolhe a AZ antes de criar o volume e, se a EC2 não subir, apaga o volume vazio e tenta outra AZ e, se preciso, um tipo equivalente (`t4g` → `m7g`).

### 8.4 Modelo de custo por cliente

Premissas (preços AWS de `sa-east-1` dos arquivos oficiais de preço, publicados em set–out/2026; o resto são **estimativas**; câmbio usado: **R$ 5,50 por US$**, revalidar):

- EC2 on-demand pela tabela 2.6; Savings Plan (−34% na EC2) avaliado à parte.
- EBS gp3 US$ 0,152/GB-mês (3.000 IOPS e 125 MiB/s incluídos); volume raiz de 8 GB por EC2; volume de dados a partir de 4 GB; snapshots US$ 0,068/GB-mês, supondo armazenado ≈ 1,5× o dado usado (incrementais de 7 dias).
- IPv4 público (inclusive elástico) US$ 0,005/h ≈ US$ 3,65/mês, cobrado em uso ou parado [fonte: aws.amazon.com/vpc/pricing].
- Saída para a internet US$ 0,15/GB nos primeiros 10 TB; ~1,5 GB/instância/mês (o tráfego EC2 → proxy residencial conta como saída) + webhooks; os 100 GB/mês grátis da conta são ignorados (pior caso).
- CloudWatch Logs US$ 0,90/GB ingerido e US$ 0,0408/GB-mês armazenado; ~50 MB de log/instância/mês; 2 alarmes por projeto (~US$ 0,10 cada, incerto); métricas de aplicação no Prometheus próprio (sem métricas customizadas no CloudWatch).
- SSM Run Command/Session Manager e DLM: sem custo para EC2 [fontes: aws.amazon.com/systems-manager/pricing; docs.aws.amazon.com/ebs/latest/userguide/snapshot-lifecycle.html].
- IP residencial: US$ 5,00/instância/mês (5.7).
- Taxa de pagamento por fatura: Pix/boleto Asaas R$ 1,99; cartão Asaas 2,99% + R$ 0,49; Stripe cartão BR 3,99% + R$ 0,39 + 0,7% Billing (+2% se internacional). (Só a partir do fim do C1.)
- Custos fixos (não rateados abaixo), ~US$ 150/mês no C1 (estimativa): control plane em `t4g.small` (US$ 19,56) + EBS + IPv4; RDS PostgreSQL `db.t4g.micro` Single-AZ (US$ 24,82) + 20 GB gp3 (US$ 4,38) + backups; Prometheus/Grafana em outra `t4g.small`; KMS (US$ 1/chave); S3, CloudTrail, CloudWatch da base; plano de suporte Developer da AWS opcional.

Custo variável por projeto (US$/mês):

| Cenário | EC2 | EBS + snapshots | IPv4 | Saída | Logs + alarmes | IPs residenciais | **Total** | **Por instância** |
|---|---|---|---|---|---|---|---|---|
| A — 1 instância, EC2 dedicada (t4g.micro) | 9,78 | 1,93 | 3,65 | 0,23 | 0,25 | 5,00 | 20,84 | **US$ 20,84 ≈ R$ 115** |
| A' — 1 instância, t4g.nano (se couber) | 4,89 | 1,93 | 3,65 | 0,23 | 0,25 | 5,00 | 15,95 | **US$ 15,95 ≈ R$ 88** |
| E — 1 instância, empacotado (C2: 25 projetos por t4g.large, 3.12) | 3,13 | 0,76 | 0,15 | 0,23 | 0,06 | 5,00 | 9,33 | **US$ 9,33 ≈ R$ 51** |
| B — 5 instâncias (t4g.small) | 19,56 | 2,71 | 3,65 | 1,13 | 0,45 | 25,00 | 52,50 | **US$ 10,50 ≈ R$ 58** |
| C — 30 instâncias (t4g.large) | 78,26 | 7,14 | 3,65 | 6,75 | 1,70 | 150,00 | 247,50 | **US$ 8,25 ≈ R$ 45** |
| D — 100 instâncias (t4g.xlarge) | 156,51 | 20,50 | 3,65 | 22,50 | 5,20 | 500,00 | 708,36 | **US$ 7,08 ≈ R$ 39** |

Custo marginal de uma instância a mais num projeto existente: ~US$ 7,00 ≈ R$ 38,50 (IP residencial 5,00 + ~150 MB de RAM ~1,50 + EBS/snapshot ~0,22 + saída 0,23 + logs 0,05) (estimativa).

Leitura:

- **O piso fixo de um projeto na AWS é ~US$ 15/mês antes do proxy** (t4g.micro + IPv4 público + EBS), contra ~US$ 6 no Fly com IPv4 compartilhado. Uma EC2 por projeto é cara para projetos de 1 instância: é o principal argumento para o empacotamento do C2 (cenário E), que traz esse projeto de R$ 115 para ~R$ 51.
- **Acima de 5 instâncias o IP residencial volta a ser ~50–70% do custo**; negociar volume com o fornecedor de proxy pesa mais que otimizar EC2.
- **Savings Plan** (−34% na EC2) depois que a base estabilizar: cenário A cai para ~US$ 17,51 (R$ 96), B para ~US$ 45,85.
- Referência de mercado por instância/mês (2026-10-17): Z-API R$ 99,99 (parceiro R$ 89,99 a partir de 10), CloudZapi R$ 97 → R$ 59 com volume, Whapi.Cloud R$ 49 (promoção), W-API R$ 19,90–29,90, Zappy a partir de R$ 14,99 [fontes: z-api.io; cloudzapi.com; whapi.cloud/pt/price; w-api.app; zappy.api.br]. Os mais baratos, até onde se vê, não incluem IP residencial dedicado (incerto).

**Preço proposto e margem de contribuição** (faixas por organização: 1ª instância R$ 99, 2ª–9ª R$ 79, 10ª–49ª R$ 69, 50ª+ R$ 59; taxa Pix de R$ 1,99 por fatura):

| Projeto | Receita/mês | Custo variável/mês | Margem de contribuição |
|---|---|---|---|
| 1 instância, EC2 dedicada (C1) | R$ 99 | R$ 115 | **−18%** (prejuízo de ~R$ 18) |
| 1 instância, t4g.nano | R$ 99 | R$ 88 | ~9% |
| 1 instância, empacotado (C2) | R$ 99 | R$ 51 | ~46% |
| 5 instâncias | R$ 415 | R$ 289 | ~30% |
| 30 instâncias | R$ 2.180 | R$ 1.361 | ~38% |
| 100 instâncias | R$ 6.500 | R$ 3.896 | ~40% |

**Piso de preço sugerido** (margem de contribuição ≥ 30% depois da taxa de pagamento):

- **Instância adicional**: nunca abaixo de **R$ 55** (custo marginal ~R$ 38,50 ÷ 0,7). As faixas propostas (R$ 79 → 59) respeitam o piso.
- **Projeto de 1 instância**: R$ 166 com EC2 dedicada, R$ 127 com t4g.nano, **R$ 75 empacotado**. Recomendação (D3): manter o preço público de **R$ 99** (paridade com a Z-API) e tornar o empacotamento (3.12, opção a) **requisito do C2** para projetos de até 3 instâncias. No beta do C1 (~20 clientes, cobrança manual) o prejuízo nos projetos de 1 instância é limitado (~R$ 18/projeto/mês) e aceitável; alternativa: no C1 aceitar só projetos com 2+ instâncias ou cobrar R$ 149 pelo projeto de 1 instância até o empacotamento existir.
- **Team**: taxa de organização sugerida de **R$ 499/mês** (SSO, auditoria, suporte prioritário, SLA 99,9% a partir do C3) + as mesmas faixas por instância. (O Supabase Team custa US$ 599/mês [fonte: supabase.com/pricing].)
- **Desconto "traga seu proxy"** (opcional, D3): −R$ 25/instância para quem fornece o próprio proxy (o IP não é do pool; o cliente assume o risco de ban).
- **Teste grátis**: ~R$ 26 por teste de 7 dias em EC2 dedicada t4g.micro, ~R$ 12 empacotado (inclui o IP de teste reaproveitado). Com conversão de 10%, R$ 120–260 de custo de aquisição por cliente pago (estimativa); mais um motivo para o teste vir junto com o empacotamento no C2.
- Preços em USD para Stripe internacional: US$ 19 / 15 / 13 / 11 nas mesmas faixas (estimativa, arredondado).

### 8.5 Riscos de infraestrutura

| Risco | Impacto | Mitigação |
|---|---|---|
| Cotas padrão da conta (5 vCPUs on-demand, 5 IPv4 elásticos) | sem elas o beta não passa de 2 projetos | pedir aumento em Service Quotas na primeira semana do C1; alarme em 80% de cada cota (8.2, 8.3) |
| Conta AWS existente compartilhada com outros workloads | disputa de cotas, custos misturados, raio de incidente maior | tags obrigatórias e Cost Explorer por tag; staging separado; contas dedicadas via AWS Organizations no C3 (D16) |
| Dependência do sslip.io para o DNS das URLs dos clientes | se o serviço cair, URLs param de resolver | certificado por IP como plano B; domínio próprio no fim do C1 e migração antes do C2 (3.7, D11) |
| Projeto de 1 instância caro na AWS (~R$ 115/mês) | margem negativa a R$ 99 | empacotamento no C2; decisão de preço do beta (D3) |
| Falha de uma AZ | projetos daquela AZ fora até restaurar o snapshot | AZs espalhadas; runbook de restauração em outra AZ testado no C1; Litestream no C3 |
| IPs da AWS classificados como datacenter | restrições ou banimentos no WhatsApp | proxy residencial obrigatório por instância (`require_proxy`, E4); IP da AWS só como contingência opt-in (5.1) |
| `sa-east-1` ~1,6× mais cara que `us-east-1` | custo maior | Savings Plan após estabilizar; empacotamento; não há região AWS alternativa no Brasil |
| Termos dos fornecedores de proxy proíbem revenda | quebra de contrato | acordo escrito de uso em SaaS antes do C1 (5.3) |
| Erro em upgrade derrubando muitos projetos | indisponibilidade em massa | ondas com controle de taxa do Run Command e parada no primeiro erro; snapshot antes; rollback testado (3.6) |

## 9. Mudanças no motor para a nuvem (backlog)

Tudo abaixo entra no motor open source (serve também a quem faz self-host); nada é exclusivo da nuvem. Esforço: **P** (até 2 dias), **M** (até 1 semana), **G** (mais de 1 semana). "Marco" diz em que fase da nuvem o item é necessário.

| # | Mudança | Por quê | Esforço | Marco |
|---|---|---|---|---|
| E1 | **Config 100% por ambiente**: `instance_defaults`, retenções de webhook, `message_retention_days`, `media_retention_days` e limites hoje só no arquivo ganham `LITEZAP_*` (ou um `LITEZAP_CONFIG_JSON`) | o container do projeto é configurado só por env (arquivo gerado do Parameter Store); sem `litezap.yaml` no volume | P | C1 |
| E2 | **Endpoint de ops separado**: `ops_listen_addr` com `/healthz`, `/readyz` e `/metrics` sem token (hoje exigem admin fora de loopback) | scrape do Prometheus pela VPC e health check sem token (protegido pelo security group) | P | C1 |
| E3 | **`/metrics` Prometheus** (já previsto na seção 9.4 do motor; não implementado) | métricas por projeto, alertas, painel | M | C1 |
| E4 | **Token de operador e settings travadas**: `operator_token` (aceita dois valores para rotação) com poder acima do admin; `locked_settings: [proxy_url, unsafe_pacing]` faz o admin receber `403 forbidden` nesses campos | o cliente pode ter o token admin sem mexer no IP nem nos pisos de ritmo | M | C1 |
| E5 | **Limite de instâncias**: `max_instances` (env), `POST /instances` acima responde `403 plan_limit` | o cliente com token admin não cria instâncias fora do plano | P | C1 |
| E6 | **Evento `instance.proxy_failed`** (WHK-9/M11) com contagem de falhas e último erro, além do `reason: proxy_unreachable` | quarentena de IP e troca automática | P | C1 |
| E7 | **Login de um clique emitido por terceiro**: `/app/api/session` aceitar um código assinado (HMAC com segredo do projeto, curto, uso único) emitido pelo control plane, não só pelo app de bandeja em loopback | reaproveitar o painel do motor dentro do painel da nuvem | M | C1 |
| E8 | **Exportação/importação**: `POST /v1/export` (admin) gera arquivo com backup online do SQLite, mídia e config, opcionalmente enviado a uma URL S3 pré-assinada; `litezap import` | backups lógicos, LGPD, migrar entre self-host e nuvem nos dois sentidos | M | C1 (exportação), C2 (importação) |
| E9 | **Logs em JSON** (`LITEZAP_LOG_FORMAT=json`) com `instance_id` e sem telefones | explorador de logs | P | C1 |
| E10 | **Allowlist de IP por token + `trusted_proxies`** (SEC-4/M11), lendo `X-Forwarded-For` do Caddy local | pedido comum de empresas | P | C2 |
| E11 | **Contadores de uso persistidos**: tabela diária por instância (enviadas, recebidas, entregas de webhook, bytes de mídia, minutos conectada) e `GET /v1/usage?from=&to=` (operador) | cobrança por uso confiável, independente de métricas | M | C1 |
| E12 | **Upgrade gracioso**: `/readyz` passa a 503 assim que o SIGTERM chega; shutdown cabe no `docker stop -t 30`; migrações compatíveis com uma versão para trás (regra de processo) | ondas de upgrade e rollback | P | C1 |
| E13 | **Watchdog de conexão zumbi** (`zombie_timeout`, pendente desde o marco 2b) | sockets "conectados" que não recebem nada são o pior caso para SLA | M | C1 |
| E14 | **API admin para plataformas** (M11): renomear instância, exclusão em lote, paginação de `GET /instances`, metadados livres por instância (`labels`) | control plane e parceiros | P | C2 |
| E15 | **Limpeza de mídia e retenção do log** (marco 2b) | custo de volume e LGPD | P | C1 |
| E16 | **Apagar chat/mensagens pela API** | direitos do titular (LGPD) | P | C2 |
| E17 | **Modo "envio suspenso"** por instância e global (`sending_disabled`, só operador): a fila aceita e segura, ou recusa com `403 sending_suspended` | cobrança atrasada e abuso, sem derrubar a recepção | P | C1 |
| E18 | **Limite global de requisições por token** (429) | proteção do projeto exposto | P | C2 |
| E19 | **Confirmação de entrega worker→supervisor** (limitação conhecida do README) | não perder eventos em crash | M | C3 |
| E20 | **Página de pareamento hospedada** (PAR-6/M11): link assinado de uso único que mostra o QR | parceiros embutem a conexão no software deles | M | C2 |
| E21 | **Projeto em modo "pausado"**: `serve` sobe sem iniciar workers (`LITEZAP_START_INSTANCES=false`) para exportação e diagnóstico | suspensão por cobrança sem apagar nada | P | C2 |
| E22 | **Imagem de container oficial multi-arquitetura** (`linux/arm64` e `amd64`), usuário não root, `HEALTHCHECK`, sinal SIGTERM tratado como shutdown gracioso | EC2 Graviton e hosts compartilhados; também serve ao self-host com Docker | P | C1 |
| E23 | **Vários hostnames em `public_url`** (lista; o primeiro é o canônico para URLs assinadas) e proteção de *Host* aceitando todos | troca do domínio temporário pelo definitivo sem quebrar clientes | P | C1 |

O `ContainerSpawner` previsto na decisão 4 do motor **não** é necessário: o `ProcessSpawner` roda dentro do container do projeto. Ele só volta à mesa se a decisão D2 mudar. Itens da revisão 1 específicos do Fly (checks e segredos em `fly.toml`, `Fly-Client-IP`, `kill_timeout`) foram reescritos acima para o equivalente na AWS; nenhum item novo do motor depende de AWS.

## 10. Roadmap

Marcos enxutos; cada um termina com clientes reais usando o que foi entregue.

### C1 — Beta fechado na AWS (~20 clientes), cobrança automática no fim

Escopo, na ordem de entrega:

1. **Conta e base**: pedidos de aumento de cota (vCPUs on-demand, IPv4 elásticos) logo no início; Terraform da base (seção 3.11), AMI com Packer, ECR, RDS, KMS, DLM, CloudTrail, Budgets.
2. **Motor**: E1–E9, E11–E13, E15, E17, E22, E23.
3. **Control plane mínimo**: Postgres, `EC2Provider`, tarefa de provisionamento idempotente, ferramenta interna `lzctl` (provisionar, redimensionar, pausar, excluir, upgrade em onda). **Provisionamento disparado manualmente pela equipe** depois de aprovar cada cliente do beta.
4. **Domínio temporário**: hostnames sslip.io com Caddy + Let's Encrypt (3.7).
5. **Painel**: login (e-mail + senha + 2FA), uma organização por conta, papéis Owner/Developer, lista de projetos e instâncias, painel do motor embutido com login de um clique (E7).
6. **Pool de IPs**: um provedor de proxy (D7), atribuição de 1 IP por instância, quarentena manual com alertas.
7. **Operação**: snapshots DLM, upgrades em ondas por SSM Run Command, logs no CloudWatch, Prometheus/Grafana, alertas, página de status.
8. **Cobrança manual durante o beta**: preço combinado por cliente, cobrado por Pix/boleto emitido manualmente (ou link de pagamento avulso do Asaas), com controle numa tabela do control plane; o control plane já registra o uso diário (E11) para conferir.
9. **Último passo do C1: gateways de pagamento**: Asaas (Pix, boleto, cartão BR) e Stripe (cartão internacional), slots de instância, webhooks idempotentes e dunning básico (seção 6). Junto com isso, registrar um domínio (mesmo provisório) e migrar os hostnames antes do C2 (3.7).

Critérios de aceite:

1. Pedido aprovado → projeto `ready` em menos de 5 minutos com um único comando do `lzctl`, em 20 criações seguidas, sem passos no console da AWS.
2. Nenhum recurso AWS sem as tags `litezap:managed`/`litezap:project`; a conciliação diária não encontra órfãos depois de 20 criações e exclusões.
3. Nenhuma porta além da 443 (e da 9090 restrita ao monitoramento) aberta; nenhum acesso SSH; um papel IAM de projeto não consegue ler parâmetros de outro projeto (teste automatizado).
4. Cada instância sai por um IP brasileiro diferente (eco pelo proxy) e nenhum IP serve a dois clientes.
5. Um upgrade do motor aplicado a todos os projetos do beta em ondas pelo Run Command, com todas as instâncias de volta a `connected` em até 2 minutos, sem novo QR; uma versão quebrada propositalmente para a onda no canário.
6. Restaurar um snapshot DLM num projeto novo, em outra AZ, recupera o log de mensagens e os webhooks.
7. Parar e iniciar a EC2 de um projeto (simulando manutenção) mantém hostname, certificado e sessões.
8. Custo real por projeto (Cost Explorer por tag `litezap:project` + proxy) dentro de ±20% do modelo da seção 8.4.
9. (Fim do C1) Pagamento por Pix, boleto, cartão BR e cartão internacional cria/atualiza a assinatura; webhook duplicado ou fora de ordem não altera o resultado; atraso leva a aviso → envio suspenso → projeto parado, e o pagamento reativa tudo sem intervenção.

### C2 — Self-service

Escopo:

- Teste grátis de 7 dias com KYC leve; assistente de criação de projeto; vários projetos e organizações por conta; convites e todos os papéis.
- Planos Pro e Team, componentes por uso (mensagens, mídia), teto de gastos, alertas de cobrança, página de uso, faturas.
- Explorador de logs e painel de métricas por projeto; página de estado por projeto.
- API de gerenciamento pública documentada (OpenAPI) + CLI `litezap cloud` (login, `projects create|list|delete`, `instances create|qr|delete`, `usage`), chaves `lzm_` com escopo por projeto. Página de pareamento hospedada (E20) para parceiros.
- Motor: E8 (importação), E10, E14, E16, E18, E20, E21.
- Ondas de upgrade automatizadas com gates por métricas; troca automática de IP com quarentena (5.5); escolha de UF/DDD.
- Domínio próprio com hostnames estáveis (Route 53), estoque de projetos pré-provisionados parados para p95 < 90 s.
- **Empacotamento** de projetos pequenos (teste e Pro até 3 instâncias) em hosts compartilhados (3.12, opção a) — requisito para o teste grátis e para a margem do projeto de 1 instância (8.4).

Critérios de aceite:

1. Um usuário novo, sem contato com a equipe, cria conta, paga e conecta um número.
2. Um script de parceiro cria 10 projetos com 1 instância cada pela API de gerenciamento e obtém os links de pareamento, sem usar o painel.
3. Com o teto de gastos ligado, nenhum excedente é cobrado e o cliente recebe os alertas de 80% e 100%.
4. A onda de upgrade para sozinha quando o canário piora a taxa de reconexão (teste com uma versão sabotada em ambiente de staging).
5. Um IP marcado como ruim é trocado automaticamente, a instância reconecta e o IP antigo vai para quarentena.

### C3 — Escala e operação

Escopo:

- Team completo: SSO/SAML, logs de auditoria visíveis, papéis por projeto, adiamento de upgrades, SLA 99,9% com créditos.
- Enterprise: conta AWS dedicada na nossa AWS Organization, contrato, suporte com SLA; desenho do BYOC (papel IAM na conta do cliente).
- Data plane numa conta AWS separada da do control plane; Savings Plan; Multi-AZ no RDS.
- Litestream do `litezap.db` (RPO de minutos para o log), exportação agendada para o bucket do cliente.
- Segundo provedor de proxy (redundância e preço), IPs móveis como add-on.
- Motor: E19; avaliação de `ContainerSpawner` se projetos acima de 128 instâncias forem comuns.
- Capacidade: automação de redimensionamento, relatórios de custo por cliente, revisão de preço.

Critérios de aceite:

1. 500 projetos ativos sem trabalho manual recorrente acima de 2 h/semana para a operação.
2. Disponibilidade medida do data plane ≥ 99,9% por 3 meses nos projetos Team.
3. Falha simulada de uma AZ: projetos afetados recriados a partir de snapshot em outra AZ em até 30 minutos (runbook testado); falha de host recuperada automaticamente sem perda de dados.
4. Falha total de um provedor de proxy: instâncias migradas para o segundo provedor em até 1 h, com aviso aos clientes.

### Depois de C3

BYOC, multi-região (outra região só se houver demanda fora do Brasil), white-label para revendas, plano Free permanente se o custo por instância cair (D4).

## 11. Decisões em aberto

Cada decisão: opções, recomendação e o que depende dela. Nenhuma está fechada.

**D1 — Data plane na AWS: uma EC2 por projeto × hosts compartilhados × ECS**
- Opções: (a) uma EC2 Graviton (`t4g`) por projeto, com EBS gp3 próprio, SG e papel IAM próprios; (b) hosts compartilhados com um container e um volume por projeto; (c) ECS em EC2 com EBS; (d) Fargate + EFS; (e) EKS.
- Recomendação: **(a) no C1** (isolamento de VM, simples de operar por SSM, cada projeto se recupera sozinho com a recuperação automática da EC2); **(a) + (b) a partir do C2**, com (b) para teste e Pro de até 3 instâncias (o projeto de 1 instância em EC2 dedicada dá prejuízo, 8.4) e (a) para Team/Enterprise. (c) não serve porque o EBS gerenciado pelo ECS é apagado quando a tarefa de um *service* termina; (d) descartado (SQLite/WAL em sistema de arquivos de rede); (e) custo fixo e operação sem retorno no nosso tamanho (3.12).
- Depende disso: provisionamento (3.3), custo e margem (8.4), cotas (8.3), isolamento (7.1).

**D2 — Modelo de processo dentro do projeto**
- Opções: (a) `ProcessSpawner` dentro do container do projeto (supervisor + workers como processos); (b) `ContainerSpawner`, um container por instância de WhatsApp com volume compartilhado.
- Recomendação: **(a)** até C3. Zero mudança no motor; o isolamento entre clientes já é por EC2 (ou por container de projeto no empacotamento). (b) só se projetos com mais de ~120 instâncias forem comuns, e exige resolver mídia por arquivo entre containers (o README já aponta essa limitação).
- Depende disso: tamanhos de compute (2.6), backlog do motor, custo.

**D3 — Preço e unidade de cobrança**
- Opções de unidade: (a) slots de instância pré-pagos (como Z-API); (b) instância-dia medida, pós-paga; (c) instância-dia só quando `connected`. Opções de preço: paridade com Z-API (R$ 99) × abaixo (R$ 69–79) × premium.
- Recomendação: **(a) slots pré-pagos** quando os gateways entrarem (fim do C1; funciona igual em Asaas e Stripe, previsível), com faixas **R$ 99 / 79 / 69 / 59** (8.4) e Team com taxa de R$ 499. Na AWS, o projeto de 1 instância em EC2 dedicada custa ~R$ 115 e dá prejuízo a R$ 99: manter R$ 99 só com o empacotamento do C2; até lá, aceitar o prejuízo limitado do beta **ou** cobrar R$ 149 pelo projeto de 1 instância **ou** exigir 2+ instâncias no beta (a escolher). Piso para instância adicional: R$ 55. (b) para parceiros no Team. Mensagens contam só envios. Desconto "traga seu proxy" a decidir depois de medir demanda.
- Depende disso: página de preços, modelo de dados de cobrança (6.2), teto de gastos, margem.

**D4 — Teste grátis e plano Free**
- Opções: (a) teste de 7 dias sem cartão, 1 instância; (b) teste com cartão; (c) Free permanente com o servidor parado quando ocioso; (d) Free permanente "só envio".
- Recomendação: **(a)** com KYC leve (e-mail + telefone) e sub-pool de IPs de teste, lançado no C2 junto com o empacotamento (teste em EC2 dedicada custa ~R$ 26; empacotado ~R$ 12). Free permanente não no lançamento (cada projeto custa servidor + IPv4 + IP residencial); o grátis permanente é o self-host. Reavaliar (c)/(d) quando houver dados de custo.
- Depende disso: abuso (7.3), custo de aquisição, pausa de projetos (2.7).

**D5 — O cliente recebe o token admin?**
- Opções: (a) nunca: só tokens de instância e a API de gerenciamento; (b) sim, um token admin "de cliente" com `proxy_url`/`unsafe_pacing` travados e `max_instances` (E4, E5), além do token de operador só nosso; (c) sim, irrestrito.
- Recomendação: **(b)**. Mantém a promessa "é o mesmo LiteZap do self-host" (painel, webhooks globais, `/v1/events`, MCP admin) sem abrir o pool de IPs. (c) é inviável (o cliente trocaria o proxy e desligaria os pisos). Mostrar o token admin só para Owner/Admin e registrar no log de auditoria.
- Depende disso: E4/E5 no motor (bloqueia C1), papéis (2.2), painel.

**D6 — Postgres do control plane**
- Opções: (a) RDS PostgreSQL em `sa-east-1`, sub-rede privada da nossa VPC (`db.t4g.micro` Single-AZ US$ 0,034/h ≈ US$ 24,82/mês + gp3 US$ 0,219/GB-mês; Multi-AZ ≈ US$ 50,37/mês); (b) Aurora PostgreSQL Serverless v2 (US$ 0,25/ACU-hora, mínimo 0 ACU com pausa automática nas versões recentes, ao custo de latência na retomada); (c) Supabase Pro em `sa-east-1` (US$ 25/mês, fora da nossa VPC, acesso pela internet); (d) Neon em `aws-sa-east-1`.
- Recomendação: **(a) RDS `db.t4g.micro` Single-AZ** no C1 (backups automáticos de 7 dias com PITR nativo, banco sem endereço público, mesma conta/fatura/IAM, latência mínima para o control plane), **Multi-AZ no C3**. Supabase deixa de ter vantagem: exigiria conexão pela internet e é outro fornecedor com dados pessoais. Aurora Serverless só se a carga ficar muito irregular.
- Depende disso: LGPD (localização), backups do control plane, custos fixos (8.4).

**D7 — Fornecedor de proxy**
- Opções: IPRoyal, Proxy-Cheap, Bright Data, móvel BR (Coronium/ProxyBrazil), outros (5.3).
- Recomendação: **IPRoyal** no C1 (com acordo escrito de uso em SaaS), Proxy-Cheap testado em paralelo pelas cidades fora de SP, **Bright Data** como segundo fornecedor no C3. Móvel só como add-on Enterprise.
- Depende disso: custo por instância (~70% do custo), casamento de UF/DDD, API de troca de IP, contrato.

**D8 — Entidade, moeda e nota fiscal**
- Opções: (a) só empresa brasileira, Asaas + Stripe Brasil, preços em BRL e USD; (b) empresa brasileira + entidade nos EUA para Stripe US.
- Recomendação: **(a)** até haver receita internacional relevante. NFS-e pelo Asaas para todos os clientes brasileiros (inclusive os que pagam pelo Stripe). Regime tributário e município com contador.
- Depende disso: taxas (6.1), emissão de notas, contratos.

**D9 — Logs e métricas**
- Opções para logs: (a) CloudWatch Logs (driver `awslogs`, US$ 0,90/GB ingerido e US$ 0,0408/GB-mês em `sa-east-1`); (b) Grafana Loki próprio; (c) Better Stack/Axiom/Grafana Cloud; (d) ClickHouse. Para métricas: (a) CloudWatch com métricas customizadas (US$ 0,30/métrica-mês); (b) Prometheus/VictoriaMetrics próprio no control plane; (c) Amazon Managed Service for Prometheus.
- Recomendação: **logs no CloudWatch** no C1–C2 (zero infraestrutura, dados no Brasil, Logs Insights alimenta o explorador do cliente), com retenção por plano e exportação para S3; reavaliar Loki/ClickHouse se o volume passar de algumas dezenas de GB/mês. **Métricas no Prometheus próprio** (scrape pela VPC, descoberta por tags EC2) + Grafana; CloudWatch só para as métricas básicas da EC2 e alarmes de infraestrutura.
- Depende disso: explorador de logs (C2), LGPD, custo (8.4).

**D10 — Estratégia de região**
- Opções: (a) só `sa-east-1`, espalhando projetos entre as 3 AZs; (b) `sa-east-1` + uma região nos EUA para clientes internacionais; (c) multi-região com failover.
- Recomendação: **(a)**. O público é brasileiro, os IPs são brasileiros, e o volume EBS preso à AZ impede failover simples; a recuperação de AZ é por snapshot (3.4). Reabrir só com demanda real fora do Brasil. `sa-east-1` custa ~1,6× `us-east-1`, mas LGPD e latência justificam.
- Depende disso: SLA (8.1), LGPD, preço.

**D11 — Domínio, TLS e ingresso**
- Opções de domínio: (a) sslip.io (`<ref>.<ip>.sslip.io`); (b) certificado Let's Encrypt para o próprio IP; (c) DNS público da EC2 (o Let's Encrypt recusa `amazonaws.com`, incerto); (d) registrar um domínio barato qualquer (~R$ 40/ano) com Route 53. Opções de TLS: Caddy na instância × ALB + ACM (o ACM exige domínio próprio; ~US$ 24,82/mês por ALB).
- Recomendação: **sslip.io + Caddy/Let's Encrypt no C1** (custo zero, troca fácil porque o hostname é dado do projeto e o motor aceitará vários hostnames, E23), com certificado por IP como plano B; **registrar um domínio no fim do C1** e migrar antes do lançamento público do C2, porque depender de um DNS gratuito de terceiros para a URL de produção dos clientes é risco de disponibilidade. ALB + ACM só com domínio próprio e hosts compartilhados (C3), se trouxer algo além de TLS.
- Depende disso: provisionamento (3.3), `public_url`, URLs assinadas de mídia, IPv4 elástico obrigatório (cota, 8.3).

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

**D15 — Infra como código**
- Opções: (a) Terraform/OpenTofu para a base + AWS SDK for Go no control plane para recursos por projeto; (b) AWS CDK (Go ou TypeScript) para a base; (c) Terraform também por projeto (um estado por cliente).
- Recomendação: **(a)** (seção 3.11). (c) não escala e tira a transação de provisionamento do control plane; (b) só se o time fosse TypeScript-first.
- Depende disso: repositório de infra, BYOC (template do papel IAM em Terraform), processo de mudança da base.

**D16 — Contas AWS**
- Opções: (a) tudo na conta existente (perfil padrão) no C1; (b) AWS Organizations com contas separadas para control plane, data plane e staging desde o início.
- Recomendação: **(a) no C1**, com tudo marcado por tag e staging isolado por VPC e prefixo de nomes; **(b) no C3** (antes de clientes Enterprise), para limitar o raio de incidentes e de cotas e separar custos. Conferir antes do beta se a conta existente tem outros workloads que disputem cotas (vCPUs, IPv4 elásticos) ou misturem custos.
- Depende disso: cotas (8.3), segurança (7.1), dedicado/BYOC (2.8).

## 12. Mapa Supabase/ClickHouse → LiteZap

O que cada padrão do Supabase Cloud e do ClickHouse Cloud vira no LiteZap, e quando entra. Descrições dos dois produtos resumidas das páginas públicas (fontes na seção 13).

| Padrão | Supabase / ClickHouse | LiteZap Cloud | Quando |
|---|---|---|---|
| Hierarquia | org → projetos (Supabase); org → services (ClickHouse) | organização → projetos → instâncias (2.1) | C1 (uma org por conta), C2 (várias) |
| Papéis | Owner/Administrator/Developer/Read-only, papéis por projeto no Team+ (Supabase); Admin/Billing/Member + papéis por serviço (ClickHouse) | Owner/Admin/Developer/Billing/Read-only (2.2) | C1: Owner/Developer; C2: todos; C3: por projeto |
| Chaves por projeto | chaves de API do projeto; tokens pessoais para a Management API | token admin do projeto, tokens `lzi_` do motor, chaves `lzm_` do control plane (2.3) | C1 |
| Assistente de criação | nome, região, compute, plano | nome, região (`sa-east-1`), tamanho, plano; manual no C1, p95 < 90 s no C2, < 60 s no C3; página de estado do projeto (2.4) | C1 (simples), C2 (completo) |
| Planos | Free / Pro US$ 25 / Team US$ 599 / Enterprise (Supabase); Basic / Scale / Enterprise (ClickHouse) | Teste / Pro / Team / Enterprise (2.5, 8.4) | C1: Pro; C2: Team e teste; C3: Enterprise |
| Componentes por uso | egress, armazenamento, MAU além da cota (Supabase); compute por minuto + armazenamento + egress (ClickHouse) | slots de instância + mensagens, mídia, add-ons de IP (6.3) | C1: slots; C2: uso |
| Teto de gastos e alertas | spend cap ligado por padrão no Pro (Supabase) | teto ligado por padrão no Pro; alertas 50/80/100% (2.5) | C2 |
| Painel de uso | página de uso por org/projeto | uso diário por projeto e instância (E11) | C2 |
| Tamanhos de compute | add-ons Micro US$ 10 a 16XL (Supabase); tamanhos em passos de 8 GiB, escala vertical automática no Scale (ClickHouse) | Nano a XL (2.6), redimensionamento automático com aviso | C1: manual; C3: automático |
| Pausa por ociosidade | Free pausado após 7 dias de pouca atividade, restaurável por 1 ano (Supabase); serviço ocioso para de cobrar compute (ClickHouse) | **não** pausa projeto pago (socket precisa ficar aberto); só o teste expirado pausa (2.7) | C2 |
| Backups e PITR | diário 7/14/30 dias por plano, PITR pago à parte (Supabase); backup por tier (ClickHouse) | snapshots diários 7/30 dias, exportação lógica, Litestream no Team (3.8) | C1: snapshots; C3: Litestream |
| BYOC / dedicado | BYOC: data plane na conta AWS/GCP/Azure do cliente, control plane do fornecedor, contrato comprometido (ClickHouse) | conta AWS dedicada (C3); BYOC com papel IAM na conta AWS do cliente (2.8) | C3 / depois |
| Self-host grátis | Supabase open source auto-hospedado | `litezap serve` / Docker, mesmo painel (2.9) | já existe |
| Explorador de logs | Logs Explorer, retenção 1/7/28/90 dias por plano (Supabase) | explorador por projeto, retenção por plano (3.9) | C2 |
| Métricas | relatórios e métricas por projeto | `/metrics` do motor + Prometheus próprio (scrape pela VPC) + gráficos no painel (3.9) | C1: interno; C2: no painel |
| Página de status | status page pública | página de status pública + estado por projeto | C1 |
| SLA por tier | 99,9% só Enterprise (Supabase); 99,9% com créditos (ClickHouse) | 99,5% Pro; 99,9% Team a partir do C3 (8.1) | C1 / C3 |
| Suporte por tier | comunidade → e-mail → SLA → TAM | comunidade → e-mail → prioritário + WhatsApp → dedicado (D12) | C1 |
| SSO/SAML | Team (Supabase, "contact us"); Enterprise (ClickHouse) | Team+ | C3 |
| Logs de auditoria | audit logs no Team+ (Supabase); activity log exportável via API (ClickHouse) | gravado desde o C1; visível e exportável no Team+ | C1 interno; C3 visível |
| API de gerenciamento | `api.supabase.com/v1` com tokens pessoais ou OAuth; `api.clickhouse.cloud/v1` com OpenAPI e chaves por org | API de gerenciamento `/v1` com OpenAPI, chaves `lzm_` com escopo | C1 interno; C2 público |
| CLI / IaC | `supabase` CLI; provedor Terraform do ClickHouse | `litezap cloud …` (subcomando do mesmo binário, só cliente HTTP); Terraform depois de C3 | C2 |
| Parceiros | (ClickHouse/Supabase: integrações OAuth) | API de gerenciamento + página de pareamento hospedada + cobrança consolidada (6.7), resposta ao Partner API da Z-API | C2 |
| Redes privadas | PrivateLink/peering no Scale+ (ClickHouse) | allowlist de IP por token (E10); IP fixo de saída para webhooks (add-on) | C2 |

A CLI ser um subcomando do binário open source (`litezap cloud`) segue o padrão do Supabase (CLI única para local e nuvem); o código do subcomando é só um cliente da API pública do control plane, sem nada fechado.

## 13. Fontes

Consultadas em 2026-10-17. Preços em US$ salvo indicação.

AWS (preços de `sa-east-1` lidos nos arquivos oficiais da Price List API, publicados entre 2026-09-11 e 2026-10-16)

- EC2 on-demand (página e arquivo de preços): https://aws.amazon.com/ec2/pricing/on-demand/ ; https://b0.p.awsstatic.com/pricing/2.0/meteredUnitMaps/ec2/USD/current/ec2-ondemand-without-sec-sel/South%20America%20(Sao%20Paulo)/Linux/index.json
- Savings Plan de computação (1 ano, sem antecipação): https://b0.p.awsstatic.com/pricing/2.0/meteredUnitMaps/computesavingsplan/USD/current/compute-savings-plan-ec2/1%20year/No%20Upfront/South%20America%20(Sao%20Paulo)/Linux/Shared/index.json
- Price List API por serviço (EC2/EBS, ELB, VPC, CloudWatch, DataTransfer, RDS, EFS): https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/<SERVICE>/current/sa-east-1/index.json
- EBS (preços e gp3): https://aws.amazon.com/ebs/pricing/ ; https://docs.aws.amazon.com/ebs/latest/userguide/general-purpose.html
- IPv4 público: https://aws.amazon.com/vpc/pricing/
- Free tier atual: https://aws.amazon.com/free/
- Systems Manager (preços): https://aws.amazon.com/systems-manager/pricing/
- Run Command com controle de taxa: https://docs.aws.amazon.com/systems-manager/latest/userguide/send-commands-velocity.html
- Data Lifecycle Manager: https://docs.aws.amazon.com/ebs/latest/userguide/snapshot-lifecycle.html
- Recuperação automática de EC2: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-recover.html
- Faixas de IP da AWS: https://docs.aws.amazon.com/vpc/latest/userguide/aws-ip-ranges.html
- Cotas: EC2 (vCPUs on-demand, `L-1216C47A`) https://docs.aws.amazon.com/ec2/latest/instancetypes/ec2-instance-quotas.html ; IPv4 elástico https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/elastic-ip-addresses-eip.html ; VPC https://docs.aws.amazon.com/vpc/latest/userguide/amazon-vpc-limits.html ; EBS https://docs.aws.amazon.com/general/latest/gr/ebs-service.html ; IAM https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html
- ACM (validação de posse do domínio): https://docs.aws.amazon.com/acm/latest/userguide/domain-ownership-validation.html
- ECS com EBS (volume apagado ao fim de tarefas de *service*): https://docs.aws.amazon.com/AmazonECS/latest/developerguide/configure-ebs-volume.html ; https://aws.amazon.com/blogs/aws/amazon-ecs-supports-a-native-integration-with-amazon-ebs-volumes-for-data-intensive-workloads/
- EFS (travas consultivas, limites): https://docs.aws.amazon.com/efs/latest/ug/limits.html
- Aurora Serverless v2 (mínimo de ACU): https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/aurora-serverless-v2.how-it-works.html

SQLite em sistema de arquivos de rede

- https://www.sqlite.org/faq.html#q5 ; https://www.sqlite.org/useovernet.html ; https://www.sqlite.org/wal.html

TLS e domínio temporário

- sslip.io / nip.io (operação, cota no Let's Encrypt): https://nip.io/
- Limites do Let's Encrypt: https://letsencrypt.org/docs/rate-limits/
- Certificados para IP (2026-01-15): https://letsencrypt.org/2026/01/15/6day-and-ip-general-availability
- Let's Encrypt e nomes `amazonaws.com` (fórum, incerto): https://community.letsencrypt.org/t/policy-forbids-issuing-for-name-on-amazon-ec2-domain/12692 ; https://community.letsencrypt.org/t/policy-forbids-issuing-name-for-aws/95246
- Domínio `.com.br` (~R$ 40/ano): https://teletime.com.br/14/10/2026/br-6-milhoes/

Fly.io (revisão 1, mantido só para comparação de custo)

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
