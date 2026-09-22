# hubizinho

Dashboard dos containers Docker do homelab. Lista tudo que está rodando como card
clicável — **com ou sem label** —, atualiza status ao vivo por SSE e funciona
100% offline: nenhum CDN, nenhuma chamada de rede saindo do host.

Binário Go único, sem dependência externa, imagem a partir do `scratch`.

## Subindo

```sh
docker compose up -d --build
```

Usando a imagem publicada pelo CI (troque `BrininhoBru`):

```sh
docker pull ghcr.io/BrininhoBru/hubizinho:latest
```

Ou sem compose:

```sh
docker run -d --name hubizinho -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  --add-host host.docker.internal:host-gateway \
  hubizinho
```

Nenhum arquivo de config é necessário. O `--add-host` não é opcional: é por ele
que o container alcança as portas publicadas no host pra checar status.

## Labels

Todas opcionais — container sem label nenhuma aparece do mesmo jeito, com o nome
do container e a menor porta TCP publicada.

| Label | Efeito |
|---|---|
| `dashboard.name` | Nome exibido no card |
| `dashboard.icon` | Nome do ícone embutido, ex.: `jellyfin` (veja `/icons/`) |
| `dashboard.url` | URL do link e alvo do healthcheck, no lugar da porta inferida |
| `dashboard.hide=true` | Esconde o container do dashboard |

```yaml
services:
  jellyfin:
    image: jellyfin/jellyfin
    labels:
      dashboard.name: Jellyfin
      dashboard.icon: jellyfin
```

## Variáveis de ambiente

| Var | Padrão | Pra quê |
|---|---|---|
| `HUB_POLL` | `5s` | Intervalo de descoberta e healthcheck |
| `HUB_ADDR` | `:8080` | Endereço de escuta |
| `HUB_HOST` | `host.docker.internal` | Host usado no healthcheck das portas publicadas |

## Coisas que vão te morder

**Container com várias portas.** Sem `dashboard.url`, o card usa a menor porta TCP
publicada. Em alguns serviços essa não é a UI — o Portainer publica 8000 (túnel do
edge agent) e 9443 (a interface). Resolve com a label:

```yaml
labels:
  dashboard.url: https://meu-host:9443
```

**Atrás de proxy reverso.** SSE morre com buffering ligado. O app já manda
`X-Accel-Buffering: no`, que resolve no nginx. No Traefik ou outro proxy, desligue
o buffering na mão (`proxy_buffering off` no nginx, se preferir explícito).

**Container pausado aparece online.** `docker pause` congela o processo mas o
kernel do container continua aceitando conexões. Limitação conhecida, não vale
código.

**Sem autenticação.** Por desenho: assume LAN confiável e um usuário só. Não
exponha na internet.

## Ícones

Subset do [dashboard-icons](https://github.com/homarr-labs/dashboard-icons)
(Apache 2.0), embutido no binário via `go:embed`. O `LICENSE` original vai junto e
é servido em `/icons/LICENSE`; as logos em si são marcas de seus donos.

`GET /icons/` lista o que está disponível. Pra adicionar: jogue o `.svg` em
`icons/` e rebuilde.

## CI

`.github/workflows/ci.yml` roda em push e em PR:

- `gofmt`, `go vet` e `go test`
- sobe a imagem `scratch` de verdade e bate em `/`, `/icons/` e `/events`
- em push na `main` ou tag `v*`, publica em `ghcr.io/BrininhoBru/hubizinho`
  (`latest`, `v1.2.3`, `sha-abc1234`)

Sem secret nenhum pra configurar: o `GITHUB_TOKEN` do runner basta.

**Na primeira publicação o pacote nasce privado**, mesmo com o repo público.
Uma vez só: página do pacote → *Package settings* → *Change visibility* → Public.

## Desenvolvendo

```sh
go test ./...
HUB_HOST=localhost go run .    # fora do container, o host é localhost mesmo
```
