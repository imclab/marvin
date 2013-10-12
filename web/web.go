package web

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"go/build"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path"

	"code.google.com/p/go.net/websocket"
	"github.com/nogiushi/marvin/nog"
)

var pkg struct {
	Version string `json:"version"`
}
var Root string
var site *template.Template
var templates = make(map[string]*template.Template)

func init() {
	if p, err := build.Default.Import("github.com/nogiushi/marvin/web", "", build.FindOnly); err == nil {
		Root = p.Dir
	} else {
		log.Println("WARNING: could not import package:", err)
	}

	if j, err := os.OpenFile(path.Join(Root, "bower.json"), os.O_RDONLY, 0666); err == nil {
		dec := json.NewDecoder(j)
		if err = dec.Decode(&pkg); err != nil {
			log.Println("WARNING: could not decode bower.json", err)
		}
		j.Close()
	} else {
		log.Println("WARNING: could not open bower.json", err)
	}

}

type longExpireHandler struct {
	h http.Handler
}

func longExpire(h http.Handler) http.Handler {
	return &longExpireHandler{h}
}

func (le *longExpireHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ttl := int64(86400)
	w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", ttl))
	le.h.ServeHTTP(w, r)
}

func getTemplate(name string) *template.Template {
	if t, ok := templates[name]; ok {
		return t
	} else {
		if site == nil {
			site = template.Must(template.ParseFiles(path.Join(Root, "templates/site.html")))
		}
		t, err := site.Clone()
		if err != nil {
			log.Fatal("cloning site: ", err)
		}
		t = template.Must(t.ParseFiles(path.Join(Root, name)))
		templates[name] = t
		return t
	}
}

type templateData map[string]interface{}

func writeTemplate(t *template.Template, d templateData, w http.ResponseWriter) {
	var bw bytes.Buffer
	h := md5.New()
	mw := io.MultiWriter(&bw, h)
	err := t.ExecuteTemplate(mw, "html", d)
	if err == nil {
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, h.Sum(nil)))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", bw.Len()))
		w.Write(bw.Bytes())
	} else {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleTemplate(prefix, name string, data templateData) {
	t := getTemplate("templates/" + name + ".html")
	http.HandleFunc(prefix, func(w http.ResponseWriter, req *http.Request) {
		d := templateData{}
		d["Title"] = name
		d["Version"] = pkg.Version
		if data != nil {
			for k, v := range data {
				d[k] = v
			}
		}
		if req.URL.Path == prefix {
			d["Found"] = true
		} else {
			w.Header().Set("Cache-Control", "max-age=10, must-revalidate")
			w.WriteHeader(http.StatusNotFound)
		}
		writeTemplate(t, d, w)
	})
}

type messageServer struct {
	nog *nog.Nog
}

func (s messageServer) wsHandler(ws *websocket.Conn) {
	messageChanges := make(chan nog.Message, 10)

	go func() {
		for message := range messageChanges {
			if err := websocket.JSON.Send(ws, message); err != nil {
				log.Println("Message Websocket send err:", err)
				break
			}
		}
	}()

	for {
		var msg nog.Message
		if err := websocket.JSON.Receive(ws, &msg); err == nil {
			if msg.Why == "register" {
				var options nog.BitOptions
				if err := json.Unmarshal([]byte(msg.What), &options); err == nil {
					s.nog.Register(messageChanges, &options)
				} else {
					log.Println("error:", err)
				}
			}
			req := ws.Request()
			who := req.RemoteAddr
			if req.TLS != nil {
				for _, c := range req.TLS.PeerCertificates {
					who = c.Subject.CommonName
				}
			}
			s.nog.In <- nog.NewMessage(who, msg.What, msg.Why)
		} else {
			log.Println("Message Websocket receive err:", err)
			break
		}
	}
	s.nog.Unregister(messageChanges)
	ws.Close()
}

func AddHandlers(m *nog.Nog) {
	handleTemplate("/", "home", templateData{"Marvin": m})

	fs := longExpire(http.FileServer(http.Dir(path.Join(Root, "static/"))))
	http.Handle("/"+pkg.Version+"/", fs)

	http.HandleFunc("/messages", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "GET" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if err := req.ParseForm(); err == nil {
				_, ok := req.Form["since"]
				if true || ok {
					log := m.Log()
					ec := json.NewEncoder(w)
					if err := ec.Encode(log); err != nil {
						return
					}

				}
			} else {
				log.Println("Error parsing form:", err)
			}
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	ms := &messageServer{nog: m}
	http.Handle("/message", websocket.Handler(ms.wsHandler))
}
