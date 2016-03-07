package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"flag"
	"fmt"
	"github.com/miekg/dns"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	http_client = http.Client{}
	dns_client  = dns.Client{}
)

type HTTPError int

func (e HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d %s", e, http.StatusText(e.Code()))
}
func (e HTTPError) Code() int {
	return int(e)
}

func should_cache(path string) bool {
	if strings.HasSuffix(path, ".pkg.tar.xz") {
		return true
	}
	if strings.HasSuffix(path, ".rpm") {
		return true
	}
	if strings.Contains(path, "/repodata/") && (strings.HasSuffix(path, ".gz") ||
		strings.HasSuffix(path, ".bz2") || strings.HasSuffix(path, ".xz")) {
		return true
	}
	return false
}

func main() {

	var (
		listen     string
		dns_server string
		data       string
		host       string
	)

	flag.StringVar(&listen, "listen", ":80", "HTTP listen address")
	flag.StringVar(&dns_server, "dns", "8.8.8.8", "DNS server to use for man in the middle mirrorlist interception")
	flag.StringVar(&data, "data", "/var/remirror", "Data storage path (data in here is public)")
	flag.StringVar(&host, "host", "9ex-dc-mirror", "This hosts name, so we can return a mirrorlist with ourselves")

	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		log.Println(r.Method + " http://" + r.Host + r.RequestURI)

		err := func() error {

			// Some special sauce mirrorlist handlers that will point to ourselves
			if r.Host == "mirrors.fedoraproject.org" {
				return fedora_mirrorlist(w, r, dns_server, host)
			}
			if r.Host == "mirrorlist.centos.org" {
				return centos_mirrorlist(w, r, dns_server, host)
			}

			// Now we guess the upstream from the URL
			upstream := ""

			if strings.HasPrefix(r.URL.Path, "/archlinux/") {
				upstream = "https://mirrors.xmission.com"
			} else if strings.HasPrefix(r.URL.Path, "/centos/") {
				upstream = "https://mirrors.xmission.com"
			} else if strings.HasPrefix(r.URL.Path, "/fedora/") {
				upstream = "https://mirrors.xmission.com"
			} else if strings.HasPrefix(r.URL.Path, "/fedora-epel/") {
				upstream = "https://mirrors.xmission.com"
			}

			if upstream == "" {
				fmt.Println("no upstream found for url", r.URL.Path)
				return HTTPError(404)
			}

			local_path := ""

			if should_cache(r.URL.Path) {
				local_path = data + path.Clean(r.URL.Path)

				stat, err := os.Stat(local_path)
				if err == nil {
					fh, err := os.Open(local_path)
					if err != nil {
						return err
					}
					defer fh.Close()

					w.Header().Set("Content-Length", strconv.Itoa(int(stat.Size())))
					w.Header().Set("Server", "remirror")
					_, err = io.Copy(w, fh)
					if err != nil {
						log.Println(err)
					}
					return nil
				}
			}

			log.Println("-->", upstream+r.RequestURI)

			req, err := http.NewRequest("GET", upstream+r.RequestURI, nil)
			if err != nil {
				return err
			}

			for k, vs := range r.Header {
				if k != "Host" {
					for _, v := range vs {
						req.Header.Add(k, v)
					}
				}
			}

			resp, err := http_client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			out := io.Writer(w)

			tmp_path := ""

			if resp.StatusCode == 200 && local_path != "" {
				tmp, err := ioutil.TempFile(data, "remirror_tmp_")
				if err != nil {
					return err
				}
				tmp_path = tmp.Name()
				//fmt.Println("tmp", tmp_path)

				defer tmp.Close()
				defer os.Remove(tmp_path)

				out = io.MultiWriter(out, tmp)
			}

			for k, vs := range resp.Header {
				for _, v := range vs {
					fmt.Printf("proxy back header %#v\t%#v\n", k, v)
					w.Header().Add(k, v)
				}
			}

			w.Header().Set("Server", "remirror")
			w.Header().Set("Content-Length", strconv.Itoa(int(resp.ContentLength)))

			w.WriteHeader(resp.StatusCode)

			n, err = io.Copy(out, resp.Body)
			if err != nil {
				log.Println(err)
				return nil
			}

			if n != resp.ContentLength {
				log.Printf("Short data returned from server (Content-Length %d received %d)\n", resp.ContentLength, n)
				return nil
			}

			if tmp_path != "" {
				os.MkdirAll(path.Dir(local_path), 0755)

				err = os.Rename(tmp_path, local_path)
				if err != nil {
					log.Println(err)
					return nil
				}
			}

			return nil
		}()

		he, ok := err.(HTTPError)
		if ok {
			http.Error(w, he.Error(), he.Code())
			fmt.Println("\t\t", he.Error())
		} else if err != nil {
			http.Error(w, err.Error(), 500)
			fmt.Println("\t\t500 " + err.Error())
		}
	})

	log.Println("arch/fedora/centos mirror proxy listening on HTTP " + listen)
	log.Fatal(http.ListenAndServe(listen, nil))
}

func centos_mirrorlist(w http.ResponseWriter, r *http.Request, dns_server, host string) error {
	err := r.ParseForm()
	if err != nil {
		return err
	}

	release := r.Form.Get("release")
	repo := r.Form.Get("repo")
	arch := r.Form.Get("arch")

	if release == "7" {
		release = "7.2.1511"
	}

	w.Header().Set("Content-Type", "text/plain")

	us := "http://" + host + "/centos/" + release + "/" + repo + "/" + arch + "/"

	if _, err := io.WriteString(w, us); err != nil {
		log.Println(err)
		return nil
	}

	log.Println("returned fudged mirrorlist " + us)
	return nil
}

func fedora_mirrorlist(w http.ResponseWriter, r *http.Request, dns_server, host, data string) error {

	err := r.ParseForm()
	if err != nil {
		return err
	}

	repo := r.Form.Get("repo")
	arch := r.Form.Get("arch")

	upstream := "mirrors.fedoraproject.org"
	if addr, err := resolve(upstream, dns_server); err != nil {
		return err
	}

	log.Println("-->", "http://"+upstream+r.RequestURI)

	if req, err := http.NewRequest("GET", "http://"+addr+r.RequestURI, nil); err != nil {
		return err
	}
	req.Header.Set("Host", upstream)

	if resp, err := http_client.Do(req); err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return HTTPError(resp.StatusCode)
	}

	if tmp, err := ioutil.ReadAll(resp.Body); err != nil {
		return err
	}

	s := string(tmp)

	start := strings.Index(s, `<resources maxconnections="1">`)
	end := strings.Index(s, `</resources>`)
	us := ""

	if start != -1 && end != -1 && repo == "epel-7" {
		us = `<url protocol="http" type="http" location="US" preference="100">http://` + host + `/fedora-epel/7/` + arch + `/repodata/repomd.xml</url>`
		s = s[:start] + us + s[end:]
	}

	w.Header.Set("Content-Length", len(s))
	w.WriteStatus(200)
	if _, err := io.WriteString(w, s); err != nil {
		log.Println(err)
		return nil
	}

	if us != "" {
		log.Println("returned fudged mirrorlist " + us)
	}

	return nil
}

func resolve(host, dns_server string) (string, error) {
	m := dns.Msg{}
	m.SetQuestion(host+".", dns.TypeA)
	dnsr, _, err := dns_client.Exchange(&m, dns_server+":53")
	if err != nil {
		return "", err
	}
	for _, ans := range dnsr.Answer {
		a, a_ok := ans.(*dns.A)
		if a_ok {
			return a.String(), nil
		}
	}
	return "", fmt.Errorf("Server not found (%#v, queried DNS server %#v)", host, dns_server)
}