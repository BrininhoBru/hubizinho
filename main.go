// hubizinho: dashboard dos containers Docker do homelab.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	socketPath  = "/var/run/docker.sock"
	dialTimeout = 1500 * time.Millisecond
	// ponytail: espera curta por EOF depois do connect; custa isso por card vivo
	// em cada ciclo, mas os dials são concorrentes. Sobe se algum serviço demorar
	// pra fechar a conexão de quem está morto.
	eofGrace = 400 * time.Millisecond
)

//go:embed index.html
var indexHTML []byte

// Subset do dashboard-icons (Apache 2.0), embutido no build. O LICENSE vai
// junto e é servido em /icons/LICENSE; /icons/ lista o que tem disponível.
//
//go:embed icons
var iconsFS embed.FS

// state é o payload do SSE: a lista inteira a cada evento, mais o erro de
// socket da §4.3 quando existe (a UI precisa distinguir "deu ruim" de "vazio").
type state struct {
	Cards []Card `json:"cards"`
	Error string `json:"error,omitempty"`
}

// As portas publicadas ficam no host, não dentro deste container: por padrão
// falamos com o gateway (compose monta o extra_hosts). HUB_HOST sobrescreve,
// ex.: "localhost" quando o binário roda direto no host.
var hubHost = env("HUB_HOST", "host.docker.internal")

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Card é o que o frontend recebe via SSE.
type Card struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Port   int    `json:"port,omitempty"`
	Icon   string `json:"icon,omitempty"`
	Online bool   `json:"online"`
	Source string `json:"source"`
}

// container é o subset de /containers/json que interessa.
type container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		PublicPort int    `json:"PublicPort"`
		Type       string `json:"Type"`
	} `json:"Ports"`
}

// ponytail: a Engine API é HTTP+JSON sobre o socket; dialer unix no lugar da lib oficial.
var docker = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	},
}

// list busca os containers em execução (o default da API já omite os parados).
func list(ctx context.Context) ([]container, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/containers/json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := docker.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker respondeu %s", resp.Status)
	}
	var cs []container
	return cs, json.NewDecoder(resp.Body).Decode(&cs)
}

// card converte um container; ok=false quando dashboard.hide esconde.
func card(c container) (Card, bool) {
	if hide, _ := strconv.ParseBool(c.Labels["dashboard.hide"]); hide {
		return Card{}, false
	}
	out := Card{
		ID:     c.ID,
		Name:   c.Labels["dashboard.name"],
		URL:    c.Labels["dashboard.url"],
		Icon:   c.Labels["dashboard.icon"],
		Source: "inferred",
	}
	for k := range c.Labels {
		if strings.HasPrefix(k, "dashboard.") {
			out.Source = "label"
			break
		}
	}
	if out.Name == "" && len(c.Names) > 0 {
		out.Name = strings.TrimPrefix(c.Names[0], "/")
	}
	// Menor porta TCP publicada. Porta não publicada não é alcançável pelo host,
	// então não vira link; o frontend monta a URL com o host que ele próprio acessou.
	for _, p := range c.Ports {
		if p.Type == "tcp" && p.PublicPort != 0 && (out.Port == 0 || p.PublicPort < out.Port) {
			out.Port = p.PublicPort
		}
	}
	return out, true
}

// cards monta a lista ordenada por nome (o Docker devolve por data de criação,
// o que faria os cards trocarem de lugar a cada ciclo).
func cards(cs []container) []Card {
	out := make([]Card, 0, len(cs))
	for _, c := range cs {
		if cd, ok := card(c); ok {
			out = append(out, cd)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// target devolve o host:porta a testar, ou "" se o card não tem o que checar.
func target(c Card) string {
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || u.Hostname() == "" {
			return ""
		}
		host := u.Hostname()
		// O check roda de dentro deste container: "localhost" aqui é o próprio
		// dashboard, não a máquina que o navegador enxerga.
		switch host {
		case "localhost", "127.0.0.1", "::1":
			host = hubHost
		}
		if p := u.Port(); p != "" {
			return net.JoinHostPort(host, p)
		}
		if u.Scheme == "https" {
			return net.JoinHostPort(host, "443")
		}
		return net.JoinHostPort(host, "80")
	}
	if c.Port != 0 {
		return net.JoinHostPort(hubHost, strconv.Itoa(c.Port))
	}
	return ""
}

// online conecta e ainda espera um instante por EOF: a porta publicada passa
// pelo docker-proxy, que aceita a conexão mesmo sem ninguém escutando dentro do
// container e só então a fecha. Só o connect diria "vivo" pra serviço morto.
// (Sem docker-proxy, o DNAT devolve RST e o próprio connect já falha.)
func online(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(eofGrace))
	_, err = conn.Read(make([]byte, 1))
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true // aberta e silenciosa: o normal de quem espera o cliente falar
	}
	return err == nil // dados = vivo; EOF/RST = não tem ninguém atrás do proxy
}

// check preenche Online com um healthcheck por card, todos em paralelo.
func check(cs []Card) {
	var wg sync.WaitGroup
	for i := range cs {
		addr := target(cs[i])
		if addr == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs[i].Online = online(addr)
		}()
	}
	wg.Wait()
}

// hub guarda o último estado publicado e os clientes SSE conectados.
type hub struct {
	mu      sync.Mutex
	state   string
	clients map[chan string]struct{}
}

// publish só acorda os clientes quando o estado muda em relação ao ciclo anterior.
func (h *hub) publish(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s == h.state {
		return
	}
	h.state = s
	for c := range h.clients {
		select {
		case c <- s:
		default: // ponytail: cliente lento perde este evento; o próximo traz o estado inteiro.
		}
	}
}

func (h *hub) subscribe() (chan string, string) {
	c := make(chan string, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients == nil {
		h.clients = map[chan string]struct{}{}
	}
	h.clients[c] = struct{}{}
	return c, h.state
}

func (h *hub) unsubscribe(c chan string) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// poll descobre, checa e publica, num ciclo só.
func (h *hub) poll(ctx context.Context, every time.Duration) {
	for {
		st := state{Cards: []Card{}}
		cs, err := list(ctx)
		if err != nil {
			log.Printf("erro lendo %s: %v", socketPath, err)
			st.Error = err.Error()
		} else {
			st.Cards = cards(cs)
			check(st.Cards)
		}
		b, err := json.Marshal(st)
		if err != nil {
			log.Println("erro serializando estado:", err)
		} else {
			h.publish(string(b))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (h *hub) events(w http.ResponseWriter, r *http.Request) {
	flush, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx/traefik sem buffering (§8)

	c, current := h.subscribe()
	defer h.unsubscribe(c)
	if current != "" { // quem chega no meio já recebe o estado corrente
		fmt.Fprintf(w, "data: %s\n\n", current)
		flush.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case s := <-c:
			fmt.Fprintf(w, "data: %s\n\n", s)
			flush.Flush()
		}
	}
}

// cacheable evita que o navegador rebusque todo ícone a cada evento SSE: o
// re-render recria os <img>, e o conteúdo é imutável dentro de um build.
func cacheable(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=86400")
		h.ServeHTTP(w, r)
	})
}

// index serve a página; "/" casa com tudo, então o resto é 404.
func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func main() {
	every, err := time.ParseDuration(env("HUB_POLL", "5s"))
	if err != nil {
		log.Fatalf("HUB_POLL inválido: %v", err)
	}
	addr := env("HUB_ADDR", ":8080")

	h := &hub{}
	go h.poll(context.Background(), every)

	http.HandleFunc("/events", h.events)
	http.Handle("/icons/", cacheable(http.FileServer(http.FS(iconsFS))))
	http.HandleFunc("/", index)
	log.Printf("hubizinho ouvindo em %s (poll %s, host %s)", addr, every, hubHost)
	log.Fatal(http.ListenAndServe(addr, nil))
}
