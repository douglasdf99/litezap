---
title: Catálogo e pedidos
description: Ler o catálogo do WhatsApp Business, enviar produtos, receber pedidos, e por que criar produtos pela API ainda não é oferecido.
---

Desde o marco 12 o LiteZap lê o catálogo do WhatsApp Business, envia produtos como cartão e recebe os pedidos
feitos pelo carrinho. É a **fase 1**: tudo o que é leitura ou mensagem comum. Criar e editar produtos continua no
celular. A referência completa está em [Catálogo e pedidos](/docs/api/catalogo/).

```bash
B=localhost:8787/v1/instances/$ID
curl -s "$B/catalog" -H "Authorization: Bearer $TOK"                    # o catálogo do próprio número
curl -s "$B/catalog?jid=5511988887777" -H "Authorization: Bearer $TOK"  # o catálogo público de outra empresa
curl -s "$B/catalog/collections" -H "Authorization: Bearer $TOK"
curl -s -X POST "$B/messages" -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"product","product_id":"8364718273645","body":"Chegou hoje!"}'
curl -s "$B/orders" -H "Authorization: Bearer $TOK"
```

## O que funciona agora

| Recurso | Como | Situação |
|---|---|---|
| Ler o catálogo (próprio ou de outra empresa) | [`GET /catalog`](/docs/api/catalogo/ler-o-catalogo/), paginado, com `catalog_link` | Pronto, aguardando validação num número real |
| Ler um produto, as coleções e a foto | `GET /catalog/products/{id}`, `/catalog/collections`, `/catalog/products/{id}/image` | Idem |
| Enviar um produto | `POST /messages` com `type: "product"` | Idem |
| Compartilhar o catálogo inteiro | Envie o `catalog_link` (`https://wa.me/c/<número>`) como texto com `link_preview: "auto"` | Pronto |
| Receber pedidos | mensagem de tipo `order` e o evento [`order.received`](/docs/eventos/order-received/) | Pronto |
| Ler os itens de um pedido | [`GET /orders/{order_id}`](/docs/api/catalogo/detalhes-de-um-pedido/) | Idem |
| Atendente IA com o catálogo | `knowledge_source: "catalog"` no perfil do agente | Pronto |
| Agentes | `get_catalog` e `get_product` em `ler`; `send_product` em `midia` | Pronto |
| Painel | aba **Catálogo** (só contas Business) | Pronto |

### Preços

O WhatsApp guarda preços como inteiros em **milésimos** da moeda: R$ 12,50 é `12500`. A API devolve os dois
formatos: `price_amount_1000` (o inteiro) e `price` (na unidade), mais `price_text` (`"R$ 12,50"`) para BRL.
Pedidos seguem a mesma regra (`total_amount_1000` e `total`).

### De onde vem o catálogo

O WhatsApp Web deixou de pedir o catálogo pelo socket: os comandos antigos (`w:biz:catalog`) ficam sem resposta.
Hoje ele usa um serviço GraphQL público (`graph.whatsapp.com/graphql/catalog`) com um token embutido no próprio
WhatsApp Web, e o LiteZap faz o mesmo. Isso tem três consequências:

- **Não depende da sessão.** A leitura não passa pela conta do WhatsApp, então o risco para o número é baixo e ela
  funciona até com o worker reiniciando.
- **Sai pela proxy da instância.** Cada leitura de um número usa a `proxy_url` efetiva dele, a mesma do worker:
  o serviço nunca vê um IP buscando catálogos de vários números. Sem proxy, a leitura é direta, como o próprio
  worker; com `require_proxy`, uma instância sem proxy recebe `409 proxy_required`.
- **Pode mudar sem aviso.** A Meta troca o token e os `doc_id` a cada versão do WhatsApp Web. Por isso eles ficam
  na config, não no código:

  ```yaml
  whatsapp_catalog:
    access_token: ""        # vazio = o valor de 2026-10-21
    doc_ids: { catalog: "", product: "", collections: "", order: "" }
  ```

  Quando a Meta mudar, a API responde `502 upstream_changed` com o que trocar; basta atualizar a config, sem nova
  versão do LiteZap.

O serviço limita por IP. O LiteZap guarda cada resposta por **5 minutos** e limita leituras novas a 30 por minuto
por instância (fotos, 60 por minuto, cada uma guardada por 5 minutos; leituras iguais e simultâneas viram uma só). Não use para varrer catálogos de terceiros em massa.

O endpoint público pode não mostrar produtos **ocultos** ou **em análise** do próprio número: produtos novos e
editados passam por revisão da Meta (`review_status: pending`) e coleções podem levar até 24 horas.

### Enviar um produto

`type: "product"` lê o produto no catálogo, baixa a primeira foto pela proxy, envia a foto como imagem normal
(cifrada, com miniatura) e monta o cartão como o WhatsApp Web (`ProductMessage` com o dono do catálogo). Vai pela
fila com as mesmas pausas, rampa e janela de qualquer envio. Produtos ocultos ou sem foto são recusados: o cliente
não conseguiria abrir; produtos ainda em análise (ou recusados) também. `catalog_owner` envia um produto de outra empresa, mas o botão "Ver" só abre se o produto
for público.

### Pedidos

Quando um cliente faz um pedido pelo carrinho, chegam uma mensagem `order` e o evento `order.received`, com o
número do pedido, a quantidade de itens e o total. O **token** do pedido fica guardado no servidor e nunca aparece
na API, nos eventos ou nos webhooks. `GET /orders/{order_id}` busca os itens primeiro pelo socket (o
`GetOrderDetails` do whatsmeow) e, se o WhatsApp não responder, pelo GraphQL; `source` diz qual respondeu.
Aceitar ou recusar o pedido é feito no app WhatsApp Business.

A mensagem de pedido é escrita pelo cliente, então o LiteZap se protege: um pedido fica preso à conversa que o
enviou (outra conversa com o mesmo número de pedido não substitui o token), e são guardados no máximo 20 pedidos
por conversa e 1000 por instância a cada 24 horas. Acima disso a mensagem ainda chega, mas sem `order.received`.

### Atendente IA com o catálogo

No perfil do agente, **Lista de produtos > Catálogo do WhatsApp** (ou `knowledge_source: "catalog"` na API) troca
os produtos digitados pelos do catálogo do número: nome, preço (o promocional, quando houver), descrição e
disponibilidade. A lista é atualizada a cada 10 minutos; se o catálogo falhar, o agente segue com a última lista
boa. Produtos ocultos ou em análise não entram. A atualização roda em segundo plano: uma resposta nunca espera
o WhatsApp (só a primeira, com até 2 segundos), e uma falha só é tentada de novo depois de 2 minutos.

Nomes e descrições do catálogo são escritos por quem edita o catálogo: o agente os recebe como **dados**, num
bloco separado das informações da empresa e com quebras de linha e marcações neutralizadas, nunca como
instruções.

## Por que criar e editar produtos ainda não é oferecido

Criar, editar, ocultar ou apagar produtos e coleções pela API **é tecnicamente possível**, mas hoje o WhatsApp
só aceita essas mudanças por outro serviço (`graph.facebook.com`), com um **token de conta de anúncios** que se
obtém por um fluxo interno do WhatsApp Web. Esse fluxo:

- liga uma identidade de anúncios ao número e pode exigir **verificação por e-mail**, que um servidor não consegue
  completar;
- tem um código de erro próprio para **bloquear a conta Business** e outro para desativar a ligação com anúncios;
- seria, de tudo o que o LiteZap faz, o comportamento mais fácil de identificar como automação.

Além disso, cada produto passa pela política de comércio da Meta, e recusas repetidas podem restringir o catálogo
ou a conta. Por isso, por enquanto:

> Crie e edite os produtos no **app WhatsApp Business** do celular, em **Ferramentas comerciais > Catálogo**.
> Eles aparecem no LiteZap em alguns minutos.

Se a fase 2 vier, será **opt-in por instância**, com aviso claro e limites duros de ritmo.

## Testar num número Business

1. No celular, crie ao menos um produto com foto (Ferramentas comerciais > Catálogo) e espere a revisão.
2. `GET /catalog`: deve listar o produto. `upstream_changed` indica token ou `doc_id` desatualizado.
3. Envie o produto para outro número seu com `type: "product"` e confira o cartão no celular (foto, preço, botão
   "Ver" abrindo o produto).
4. Do outro número, abra o catálogo, adicione ao carrinho e envie o pedido: deve chegar `order.received`, e
   `GET /orders/{order_id}` deve trazer os itens.
