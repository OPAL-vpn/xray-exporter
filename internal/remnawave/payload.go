package remnawave

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// errNotArray is static: the response body can carry subscription data, so it
// must never become part of an error string.
var errNotArray = errors.New("response body is not a JSON array")

// parsePayload accepts only a top-level JSON array. An empty array is a
// successful empty set, while null, an object, a scalar or an empty body is an
// error.
func parsePayload(body []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errNotArray
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, fmt.Errorf("malformed JSON: %w", err)
	}
	if entries == nil {
		entries = []json.RawMessage{}
	}
	return entries, nil
}

// canonical re-encodes one panel entry, so a byte-identical panel response
// always yields byte-identical configs: object keys sorted, no insignificant
// whitespace, and numbers kept verbatim instead of becoming float64.
func canonical(raw json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// remark returns the panel's remarks label, or "" when it is missing or not a
// non-empty string.
func remark(raw json.RawMessage) string {
	var entry struct {
		Remarks string `json:"remarks"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return ""
	}
	return entry.Remarks
}

// endpointHost is the address/port pair of the first outbound host.
type endpointHost struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// endpoint returns "<address>-<port>" of the first outbound, or "" when the
// entry has no usable vnext or servers host. Entries are panel-controlled and
// may be anything, so a shape mismatch yields "" rather than an error.
func endpoint(raw json.RawMessage) string {
	var entry struct {
		Outbounds []struct {
			Settings struct {
				Vnext   []endpointHost `json:"vnext"`
				Servers []endpointHost `json:"servers"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil || len(entry.Outbounds) == 0 {
		return ""
	}

	settings := entry.Outbounds[0].Settings
	hosts := settings.Vnext
	if len(hosts) == 0 {
		hosts = settings.Servers
	}
	if len(hosts) == 0 || hosts[0].Address == "" || hosts[0].Port == 0 {
		return ""
	}
	return fmt.Sprintf("%s-%d", hosts[0].Address, hosts[0].Port)
}
