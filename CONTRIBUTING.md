# Contribuindo com o LiteZap

Obrigado pelo interesse! Issues e pull requests são bem-vindos.

## Antes de começar

- Para mudanças grandes (novo recurso, mudança de API), abra uma issue antes para alinharmos o desenho.
- Bugs de segurança **não** vão em issue pública: veja [SECURITY.md](SECURITY.md).
- Ao participar, você concorda com o [Código de Conduta](CODE_OF_CONDUCT.md).

## Development setup

Requisitos: Go (versão em `go.mod`) e, para o site, Node 24 + pnpm.

```bash
go build -o litezap ./cmd/litezap
./litezap serve
```

O app de bandeja (`./cmd/litezap-app`) precisa de CGO no macOS; veja "App de bandeja" no README.

## Checks

Rode antes de abrir o PR (o CI roda o mesmo):

```bash
gofmt -l .                  # não deve listar nada
go vet ./...
go test -race -p 2 ./...
go tool buf generate        # se mudou proto/; commite o que mudar em gen/
cd site && pnpm install && pnpm build   # se mudou o site ou site/public/openapi.yaml
```

## Pull requests

- Um assunto por PR, com testes para o que mudou.
- Mensagens de commit no estilo do histórico (Conventional Commits, em português): `feat(api): ...`, `fix(worker): ...`, `docs: ...`, `chore(release): ...`.
- Mudou rota, evento ou configuração? Atualize o README e `site/public/openapi.yaml` no mesmo PR.
- Contribuições são licenciadas sob a [Apache-2.0](LICENSE), a mesma licença do projeto.
