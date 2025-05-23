package server

import (
	"encoding/json"
	"net/http"
	
	"github.com/oarkflow/dag/nlp/config"
	"github.com/oarkflow/dag/nlp/tokenizer"
)

func Run(addr, dataDir string) error {
	cfg, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	http.HandleFunc("/tokenize", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Text string }
		json.NewDecoder(r.Body).Decode(&req)
		toks := tokenizer.TokenizeSubwords(req.Text)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tokens": toks,
			"config": cfg,
		})
	})
	return http.ListenAndServe(addr, nil)
}
