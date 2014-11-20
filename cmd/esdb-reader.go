package main

import (
	"github.com/customerio/esdb/cluster"
	"github.com/customerio/esdb/stream"

	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
)

var node = flag.String("n", "localhost:4001", "node to read from")
var host = flag.String("h", "localhost", "hostname")
var port = flag.Int("p", 4002, "port")

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [arguments] <data-path> \n", os.Args[0])
		flag.PrintDefaults()
	}
}

func main() {
	log.SetFlags(0)

	flag.Parse()

	// Set the data directory.
	if flag.NArg() == 0 {
		flag.Usage()
		log.Fatal("Data path argument required")
	}

	log.SetFlags(log.LstdFlags)

	client := cluster.NewLocalClient("http://" + *node)
	reader := cluster.NewReader(flag.Arg(0))

	http.HandleFunc("/events", func(w http.ResponseWriter, req *http.Request) {
		req.Body.Close()

		var count int
		var err error

		index := req.FormValue("index")
		value := req.FormValue("value")
		continuation := req.FormValue("continuation")
		limit, _ := strconv.Atoi(req.FormValue("limit"))

		meta, con, err := client.Offset(index, value)
		if err != nil {
			write(w, 500, map[string]interface{}{
				"error": err.Error(),
			})

			return
		}

		reader.Update(meta.Peers, meta.Closed, meta.Current)

		events := make([]string, 0, limit)

		if limit == 0 {
			limit = 20
		}

		if index != "" {
			if continuation == "" {
				continuation = con
			}

			continuation, err = reader.Scan(index, value, continuation, func(e *stream.Event) bool {
				count += 1
				events = append(events, string(e.Data))
				return count < limit
			})
		} else {
			continuation, err = reader.Iterate(continuation, func(e *stream.Event) bool {
				count += 1
				events = append(events, string(e.Data))
				return count < limit
			})
		}

		res := map[string]interface{}{
			"events":       events,
			"continuation": continuation,
			"most_recent":  meta.MostRecent,
		}

		if err != nil {
			res["error"] = err.Error()
			write(w, 500, res)
		}

		write(w, 200, res)
	})

	err := http.ListenAndServe(fmt.Sprintf("%s:%d", *host, *port), nil)
	if err != nil {
		log.Fatal(err)
	}
}

func write(w http.ResponseWriter, code int, body map[string]interface{}) {
	w.WriteHeader(code)
	js, _ := json.MarshalIndent(body, "", "  ")
	w.Write(js)
	w.Write([]byte("\n"))
}