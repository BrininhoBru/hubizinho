FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
# Estático: o binário roda sozinho no scratch, sem libc.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /hubizinho .

# O binário já carrega index.html e os ícones via go:embed, então não sobra nada
# pra imagem final. O Docker monta /etc/hosts e /etc/resolv.conf mesmo no scratch,
# que é o que host.docker.internal precisa pra resolver.
FROM scratch
COPY --from=build /hubizinho /hubizinho
EXPOSE 8080
ENTRYPOINT ["/hubizinho"]
