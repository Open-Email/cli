package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newAPICmd(a *app) *cobra.Command {
	var (
		data, rawBody string
		headers       []string
		queries       []string
	)
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path>",
		Short: "Call any core API route directly (escape hatch)",
		Long: "Make an arbitrary authenticated request to core. The path is relative to\n" +
			"/api/v1 (with or without the prefix). Response status goes to stderr, body to\n" +
			"stdout.\n\n" +
			"  openemail api GET /mailboxes\n" +
			"  openemail api POST /routes -d '{\"address\":\"a@b\",\"destinationType\":\"group\"}'",
		Args: cobra.RangeArgs(2, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}

			method := strings.ToUpper(args[0])
			path := normalizeAPIPath(args[1])

			q := url.Values{}
			for _, kv := range queries {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return usageError(fmt.Errorf("bad --query %q: expected k=v", kv))
				}
				q.Add(k, v)
			}

			hdr := map[string]string{}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					return usageError(fmt.Errorf("bad -H %q: expected 'Key: Value'", h))
				}
				hdr[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}

			var body []byte
			switch {
			case rawBody != "":
				b, rerr := os.ReadFile(rawBody)
				if rerr != nil {
					return rerr
				}
				body = b
			case data != "":
				if strings.HasPrefix(data, "@") {
					b, rerr := os.ReadFile(data[1:])
					if rerr != nil {
						return rerr
					}
					body = b
				} else {
					body = []byte(data)
				}
				if _, ok := hdr["Content-Type"]; !ok {
					hdr["Content-Type"] = "application/json"
				}
			}

			resp, err := client.RawRequest(cmd.Context(), method, path, q, hdr, body)
			if err != nil {
				return err
			}
			a.out.Msgf("%s %s → %d", method, path, resp.Status)
			writeAPIBody(resp.Body)
			if resp.Status >= 400 {
				return silentExit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "request body (JSON literal, or @file); sets Content-Type: application/json")
	cmd.Flags().StringVar(&rawBody, "raw-body", "", "send this file's bytes as the body verbatim")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "extra request header 'Key: Value' (repeatable)")
	cmd.Flags().StringArrayVar(&queries, "query", nil, "query parameter k=v (repeatable)")
	return cmd
}

// normalizeAPIPath accepts a path with or without the /api/v1 prefix and returns
// a leading-slash path relative to /api/v1.
func normalizeAPIPath(p string) string {
	p = strings.TrimPrefix(p, "/api/v1")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// writeAPIBody pretty-prints a JSON body (indented) or streams it verbatim.
func writeAPIBody(body []byte) {
	if len(body) == 0 {
		return
	}
	var pretty json.RawMessage
	if json.Unmarshal(body, &pretty) == nil {
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if enc.Encode(pretty) == nil {
			fmt.Fprint(os.Stdout, buf.String())
			return
		}
	}
	os.Stdout.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		fmt.Fprintln(os.Stdout)
	}
}
