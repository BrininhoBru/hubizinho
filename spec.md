# Spec: Homelab Dashboard

**Status:** Rascunho
**Autor:** bcampos
**Data:** 2026-09-21
**Stack:** Go (binário único, single container)

---

## 1. Visão Geral

Dashboard web self-hosted que lista, como cards clicáveis, os containers Docker rodando num único host de homelab. Descobre containers automaticamente via `/var/run/docker.sock`, lê metadados de labels quando presentes, e ainda assim lista containers sem label nenhuma (com dados inferidos). Roda como um único container, 100% offline (sem CDN), com status online/offline atualizado ao vivo via SSE, sem reload de página.

## 2. Problema / Motivação

Pesquisa comparativa de 9 dashboards self-hosted existentes (Homepage, Dashy, Homarr, Heimdall, Flame, Homer, Glance, Organizr, Fenrus) mostrou que nenhum atende simultaneamente aos requisitos: (a) listar containers sem exigir label, (b) status ao vivo sem reload, e (c) funcionar 100% offline sem CDN por padrão. Os dois mais próximos (Homarr, Glance) falham em pelo menos um desses três pontos. Construir um dashboard próprio, mínimo e sob medida, resolve exatamente esses três pontos sem as ressalvas encontradas na pesquisa.

## 3. Casos de Uso

| # | Ator | Ação | Resultado Esperado |
|---|------|------|--------------------|
| 1 | Usuário | Abre o dashboard no navegador na LAN | Vê cards de todos os containers rodando, com ou sem label |
| 2 | Usuário | Sobe um novo container (com ou sem label) | Card aparece sozinho no dashboard, sem restart/reconfig |
| 3 | Usuário | Container cai ou fica não-responsivo | Card muda de status (online→offline) automaticamente, sem reload de página |
| 4 | Usuário | Adiciona labels `dashboard.*` num container | Card passa a mostrar nome/ícone/URL customizados |
| 5 | Usuário | Marca um container com `dashboard.hide=true` | Container não aparece na lista |

## 4. Requisitos Funcionais

### 4.1 Funcionalidades Principais
- [ ] RF-01: Ler lista de containers em execução via Docker Engine API (socket local `/var/run/docker.sock`), em polling periódico (ex.: a cada 5-10s) no backend.
- [ ] RF-02: Para cada container, extrair labels `dashboard.name`, `dashboard.icon`, `dashboard.url`, `dashboard.hide`.
- [ ] RF-03: Container sem nenhuma label `dashboard.*` aparece mesmo assim, com nome = nome do container e URL inferida de `http://<docker-host-ip>:<primeira porta exposta>`.
- [ ] RF-04: Container com `dashboard.hide=true` é excluído da lista (opt-out).
- [ ] RF-05: Checar status online/offline de cada serviço via TCP connect na porta (exposta ou inferida via label `dashboard.url`), em intervalo curto e configurável.
- [ ] RF-06: Expor um endpoint SSE (`/events`) que empurra o estado atual (lista de cards + status) pro navegador sempre que houver mudança (novo container, container sumiu, status mudou).
- [ ] RF-07: Frontend puro (HTML/CSS/JS vanilla, servido pelo próprio binário Go) consome o SSE e atualiza a grade de cards em tempo real, sem reload de página.
- [ ] RF-08: Servir um set de ícones vendorizado (embutido na imagem no build via `go:embed`, subset do dashboard-icons) e resolver `dashboard.icon=nome` para o arquivo local correspondente; sem ícone = fallback visual simples (sem CDN externo).

### 4.2 Regras de Negócio
- RN-01: Ausência de label nunca esconde um container — só `dashboard.hide=true` esconde (modelo opt-out, não opt-in).
- RN-02: Toda comunicação de assets/ícones/fontes/JS é servida pelo próprio binário; nenhuma chamada de rede sai do host em runtime.
- RN-03: Sem autenticação — dashboard assume rede LAN confiável, single user.
- RN-04: Sem persistência de estado (sem banco, sem arquivo de config editável em runtime) — todo estado é derivado ao vivo do Docker socket a cada ciclo.

### 4.3 Validações e Tratamento de Erros
| Situação | Comportamento Esperado |
|----------|------------------------|
| Docker socket inacessível/permissão negada | Log de erro claro no stdout do container; dashboard sobe mas mostra estado vazio/mensagem de erro na UI |
| Container sem nenhuma porta exposta e sem `dashboard.url` | Aparece na lista sem link clicável (card "não navegável", só informativo) |
| `dashboard.icon` referencia ícone que não existe no set vendorizado | Cai no fallback visual (ex: inicial do nome) |
| Múltiplas portas expostas, sem label indicando qual usar | Usa a primeira porta TCP exposta (menor número) como padrão |

## 5. Requisitos Não-Funcionais

- Performance: uso de RAM em idle na casa de dezenas de MB (Go binário único); CPU praticamente zero fora dos ciclos de poll/healthcheck.
- Simplicidade operacional: single container, zero arquivo de config obrigatório — tudo via labels + variáveis de ambiente opcionais (ex: intervalo de poll).
- Portabilidade: roda em x86_64 (hardware alvo: i7-4510U, 4 vCPU, 7.1GB RAM), sem dependência de GPU/hardware específico.
- Offline: nenhuma chamada de rede externa à LAN em nenhum momento de execução normal.

## 6. Arquitetura / Design Técnico

- Backend em Go, usando a lib oficial `docker/docker/client` pra falar com o socket.
- Loop de discovery: a cada N segundos, lista containers via `ContainerList`, extrai labels, monta a lista de "cards" em memória (sem persistência).
- Loop de healthcheck: TCP dial concorrente (goroutines) pra cada card com porta conhecida, timeout curto (ex: 1-2s), resultado guardado junto do card.
- Quando o estado (lista de cards + status) muda em relação ao ciclo anterior, o servidor publica o novo estado pros clientes conectados via SSE (`text/event-stream`).
- Frontend: página HTML única servida em `/`, JS vanilla conecta em `/events` via `EventSource`, re-renderiza a grade de cards no DOM a cada evento recebido.
- Ícones: pasta `icons/` embutida na imagem via `go:embed`, mapeamento nome→arquivo (subset curado do dashboard-icons, licença permissiva verificada no build).

### Estrutura de Dados (card, formato do payload SSE)
```json
{
  "id": "container_id",
  "name": "Jellyfin",
  "url": "http://192.168.1.50:8096",
  "icon": "jellyfin",
  "online": true,
  "source": "label"
}
```
(`source`: `"label"` quando veio de `dashboard.*`, `"inferred"` quando veio só do nome/porta do container.)

### Endpoints / Interfaces
| Método | Rota | Descrição |
|--------|------|-----------|
| GET | `/` | Página HTML da dashboard |
| GET | `/events` | Stream SSE com o estado atual e atualizações |
| GET | `/icons/{nome}.svg` | Serve ícone vendorizado embutido no binário |

## 7. Fora do Escopo

- Multi-host / múltiplos daemons Docker remotos.
- Agrupamento/organização de cards em seções.
- Autenticação/login.
- Persistência de preferências do usuário (ordenação manual, ocultar via UI).
- Outros tipos de card além de containers Docker (links manuais, systemd, etc.).
- Health-check HTTP configurável (fica só TCP connect nesta v1).

## 8. Dependências e Riscos

| Item | Tipo | Impacto | Mitigação |
|------|------|---------|-----------|
| Acesso ao `/var/run/docker.sock` | Dependência | Alto — sem isso o app não funciona | Documentar mount obrigatório no docker-compose; falhar com log claro se ausente |
| Licença do set de ícones vendorizado | Risco | Baixo (uso pessoal) | Escolher set com licença permissiva (MIT/CC) antes de embutir |
| SSE atrás de proxy reverso com buffering | Risco | Médio — pode quebrar live-update se o usuário colocar Nginx/Traefik na frente sem desabilitar buffering | Documentar a config de proxy necessária (`proxy_buffering off` etc.) se/quando for usar |

## 9. Critérios de Aceite (DoD)

- [ ] Container sobe standalone via `docker run`/`docker-compose` só com o socket montado, sem nenhum arquivo de config obrigatório.
- [ ] Container Docker sem label nenhuma aparece no dashboard com nome + porta.
- [ ] Container com labels `dashboard.*` aparece com nome/ícone/URL customizados.
- [ ] Subir um novo container aparece no dashboard sem restart do dashboard, dentro do intervalo de poll configurado.
- [ ] Derrubar um container muda o status pra offline no navegador sem reload de página (validar via DevTools Network/EventSource).
- [ ] Nenhuma requisição de rede externa à LAN acontece durante uso normal (validar via DevTools Network).
- [ ] Imagem final roda em single container, tamanho razoável (referência: compará-la aos ~6-10MB do Homer/Glance).
