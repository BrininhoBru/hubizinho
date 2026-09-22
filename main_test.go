package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Resposta de /containers/json cobrindo os casos da spec.
const fixture = `[
 {"Id":"a1","Names":["/jellyfin"],
  "Labels":{"dashboard.name":"Jellyfin","dashboard.icon":"jellyfin","dashboard.url":"http://nas:8096"},
  "Ports":[{"PublicPort":8096,"Type":"tcp"}]},
 {"Id":"b2","Names":["/pihole"],"Labels":{},
  "Ports":[{"PublicPort":8080,"Type":"tcp"},{"PublicPort":53,"Type":"udp"},{"PublicPort":443,"Type":"tcp"}]},
 {"Id":"c3","Names":["/secreto"],"Labels":{"dashboard.hide":"true"},
  "Ports":[{"PublicPort":9000,"Type":"tcp"}]},
 {"Id":"d4","Names":["/worker"],"Labels":{},"Ports":[]}
]`

func TestCards(t *testing.T) {
	var cs []container
	if err := json.Unmarshal([]byte(fixture), &cs); err != nil {
		t.Fatal(err)
	}
	got := cards(cs)

	// RF-04: dashboard.hide=true some da lista; os outros três ficam, ordenados por nome.
	if len(got) != 3 {
		t.Fatalf("esperava 3 cards, veio %d: %+v", len(got), got)
	}

	// RF-02: labels dashboard.* mandam no nome/ícone/URL.
	if c := got[0]; c.Name != "Jellyfin" || c.Icon != "jellyfin" ||
		c.URL != "http://nas:8096" || c.Source != "label" {
		t.Errorf("card com label: %+v", c)
	}

	// RF-03 + §4.3: sem label nenhuma, nome = container e porta = menor TCP publicada
	// (443 < 8080; a UDP 53 é ignorada).
	if c := got[1]; c.Name != "pihole" || c.Port != 443 || c.URL != "" || c.Source != "inferred" {
		t.Errorf("card inferido: %+v", c)
	}

	// §4.3: sem porta e sem dashboard.url, o card existe mas não é navegável.
	if c := got[2]; c.Name != "worker" || c.Port != 0 || c.URL != "" {
		t.Errorf("card sem porta: %+v", c)
	}
}

func TestTarget(t *testing.T) {
	defer func(old string) { hubHost = old }(hubHost) // global: devolve pros outros testes
	hubHost = "hostgw"
	for _, tc := range []struct {
		card Card
		want string
	}{
		{Card{URL: "http://nas:8096"}, "nas:8096"},          // porta explícita na label
		{Card{URL: "https://nas/app"}, "nas:443"},           // sem porta: default do scheme
		{Card{URL: "http://nas/app"}, "nas:80"},             //
		{Card{Port: 443}, "hostgw:443"},                     // porta publicada, via gateway do host
		{Card{URL: "http://nas:8096", Port: 9}, "nas:8096"}, // label ganha da porta inferida
		{Card{}, ""}, // nada a checar (card informativo)
	} {
		if got := target(tc.card); got != tc.want {
			t.Errorf("target(%+v) = %q, queria %q", tc.card, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	// Os listeners são locais e target() reescreve 127.0.0.1 pro host do docker.
	defer func(old string) { hubHost = old }(hubHost)
	hubHost = "127.0.0.1"

	// Serviço normal: aceita e fica calado esperando o cliente falar.
	viva, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer viva.Close()
	go func() {
		for {
			c, err := viva.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	// docker-proxy sem upstream: aceita e fecha na hora. Só o connect acharia
	// que está vivo — é o caso que quebrava o RF-05 com porta publicada.
	proxyOrfao, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyOrfao.Close()
	go func() {
		for {
			c, err := proxyOrfao.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// Ninguém escutando: connect recusado.
	livre, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	recusa := livre.Addr().String()
	livre.Close()

	cs := []Card{
		{Name: "viva", URL: "http://" + viva.Addr().String()},
		{Name: "orfa", URL: "http://" + proxyOrfao.Addr().String()},
		{Name: "recusada", URL: "http://" + recusa},
		{Name: "sem porta"},
	}
	check(cs)

	if !cs[0].Online {
		t.Error("serviço escutando devia estar online")
	}
	for _, c := range cs[1:] {
		if c.Online {
			t.Errorf("%q devia estar offline: %+v", c.Name, c)
		}
	}
}

func TestHubPublish(t *testing.T) {
	h := &hub{}
	c, current := h.subscribe()
	if current != "" {
		t.Errorf("hub novo não devia ter estado: %q", current)
	}

	h.publish(`{"cards":[]}`)
	select {
	case got := <-c:
		if got != `{"cards":[]}` {
			t.Errorf("recebeu %q", got)
		}
	default:
		t.Fatal("cliente não recebeu o primeiro estado")
	}

	// RF-06: estado igual ao ciclo anterior não vira evento.
	h.publish(`{"cards":[]}`)
	select {
	case got := <-c:
		t.Errorf("estado repetido não devia publicar, veio %q", got)
	default:
	}

	h.publish(`{"cards":[{"id":"a1"}]}`)
	if got := <-c; got != `{"cards":[{"id":"a1"}]}` {
		t.Errorf("mudança de estado: recebeu %q", got)
	}

	// Quem conecta depois recebe o estado corrente, não espera o próximo ciclo.
	_, current = h.subscribe()
	if current != `{"cards":[{"id":"a1"}]}` {
		t.Errorf("cliente novo recebeu %q", current)
	}

	h.unsubscribe(c)
	h.publish(`{"cards":[]}`)
	if len(h.clients) != 1 {
		t.Errorf("esperava 1 cliente após unsubscribe, tem %d", len(h.clients))
	}
}

func TestIndex(t *testing.T) {
	// A página é servida pelo binário (RF-07) e "/" não pode virar catch-all.
	for _, tc := range []struct {
		path, tipo string
		code       int
	}{
		{"/", "text/html; charset=utf-8", 200},
		{"/qualquer-coisa", "", 404},
	} {
		w := httptest.NewRecorder()
		index(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Errorf("GET %s = %d, queria %d", tc.path, w.Code, tc.code)
		}
		if tc.code == 200 {
			if ct := w.Header().Get("Content-Type"); ct != tc.tipo {
				t.Errorf("content-type %q", ct)
			}
			if !bytes.Contains(w.Body.Bytes(), []byte("EventSource('/events')")) {
				t.Error("a página não conecta no SSE")
			}
		}
	}
}

func TestIcons(t *testing.T) {
	srv := http.FileServer(http.FS(iconsFS))
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/icons/nginx.svg", 200},     // RF-08: resolve dashboard.icon=nginx
		{"/icons/LICENSE", 200},       // atribuição da Apache 2.0 vai junto
		{"/icons/naoexiste.svg", 404}, // §4.3: 404 é o que dispara a inicial na UI
	} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Errorf("GET %s = %d, queria %d", tc.path, w.Code, tc.code)
		}
		if tc.code == 200 && w.Body.Len() == 0 {
			t.Errorf("GET %s veio vazio", tc.path)
		}
	}
	if ct := func() string {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", "/icons/nginx.svg", nil))
		return w.Header().Get("Content-Type")
	}(); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("content-type do svg = %q", ct)
	}
}
