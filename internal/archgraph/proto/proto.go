// Package proto extracts archgraph's "RPC contract" surface from the
// .proto definitions a service repository depends on.
//
// Kachō keeps every .proto under a single central module, kacho-proto
// (proto/kacho/cloud/<domain>/v1/*.proto); a service repository imports the
// generated Go stubs and pins the module through a `replace
// github.com/PRO-Robotech/kacho-proto => ../kacho-proto` directive in its
// go.mod. archgraph's L3 generator wants, per gRPC rpc, the contract a
// reader of the documentation needs: the fully-qualified method name
// (<package>.<Service>/<Method>), the request and response message types,
// and — for an rpc carrying a (google.api.http) annotation — its REST verb
// and path.
//
// # What this package does
//
//   - ResolveProtoDir reads a service repo's go.mod, follows the
//     kacho-proto replace directive to the module on disk and returns its
//     proto/ subdirectory. A repo with no such directive (corelib, the
//     api-gateway, a library) resolves nothing — the caller then renders an
//     empty / unresolved contract section, never an error.
//   - Extract walks a proto/ tree, parses every .proto file with the
//     pure-Go go-protoparser, and returns one RPC per gRPC rpc.
//
// The REST verb and path come from the standard (google.api.http) option's
// message literal: its first key is the verb (get / post / put / patch /
// delete) or "custom", and the associated string value is the path. An rpc
// without that option — every Internal-service method — yields empty REST
// fields.
//
// This package depends only on the standard library and
// github.com/yoheimuta/go-protoparser/v4. It imports no gRPC runtime, no
// persistence code and nothing from archgraph's gen layer.
package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	protoparser "github.com/yoheimuta/go-protoparser/v4"
	"github.com/yoheimuta/go-protoparser/v4/parser"
)

// protoModulePath is the Go module path of Kachō's central proto module.
// ResolveProtoDir follows the service repo's `replace` of this path.
const protoModulePath = "github.com/PRO-Robotech/kacho-proto"

// protoSubdir is the subdirectory of the kacho-proto module that holds the
// .proto tree (proto/kacho/cloud/<domain>/v1/*.proto).
const protoSubdir = "proto"

// RPC is one gRPC method extracted from a .proto file: its fully-qualified
// name and the contract archgraph's L3 "RPC contract" table renders.
type RPC struct {
	// FQN is the fully-qualified method name,
	// "<proto-package>.<Service>/<Method>", e.g.
	// "kacho.cloud.vpc.v1.NetworkService/Create". It is the key the L3
	// generator matches an L2 note's rpc anchor against.
	FQN string

	// Service is the bare service name, e.g. "NetworkService".
	Service string

	// Method is the bare method name, e.g. "Create".
	Method string

	// Request is the request message type as written in the rpc, e.g.
	// "CreateNetworkRequest".
	Request string

	// Response is the response message type as written in the rpc, e.g.
	// "operation.Operation".
	Response string

	// RESTVerb is the upper-cased HTTP verb of the rpc's (google.api.http)
	// annotation — "GET", "POST", "PUT", "PATCH", "DELETE" or "CUSTOM" —
	// or "" when the rpc carries no annotation.
	RESTVerb string

	// RESTPath is the HTTP path of the (google.api.http) annotation, or ""
	// when the rpc carries no annotation.
	RESTPath string
}

// ResolveProtoDir locates the proto/ directory of the kacho-proto module a
// service repository at repoRoot depends on.
//
// It reads repoRoot/go.mod, finds a `replace github.com/PRO-Robotech/
// kacho-proto => <target>` directive, resolves <target> (relative to
// repoRoot) and returns <target>/proto. It reports ok=false when go.mod is
// absent or carries no kacho-proto replace, or when the resolved proto/
// directory does not exist — every "no proto module" case the caller
// handles gracefully.
func ResolveProtoDir(repoRoot string) (dir string, ok bool) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return "", false
	}
	target, found := replaceTarget(string(data), protoModulePath)
	if !found {
		return "", false
	}
	// A replace target is a module-relative-or-absolute path; resolve it
	// against the repo root.
	if !filepath.IsAbs(target) {
		target = filepath.Join(repoRoot, target)
	}
	protoDir := filepath.Join(target, protoSubdir)
	info, err := os.Stat(protoDir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return protoDir, true
}

// replaceTarget scans go.mod text for a `replace <module> => <target>`
// directive — in either the single-line or the block form — and returns
// the target path. A `replace <module> <ver> => <target> <ver>` form is
// handled by taking the last whitespace-separated token of the right-hand
// side. It reports found=false when the module has no replace directive.
func replaceTarget(goMod, module string) (target string, found bool) {
	var inBlock bool
	for _, raw := range strings.Split(goMod, "\n") {
		line := strings.TrimSpace(raw)
		// Strip a trailing line comment.
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "replace (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		}

		spec := line
		if !inBlock {
			rest, ok := strings.CutPrefix(line, "replace ")
			if !ok {
				continue
			}
			spec = strings.TrimSpace(rest)
		}

		lhs, rhs, ok := strings.Cut(spec, "=>")
		if !ok {
			continue
		}
		// The LHS is "<module>" or "<module> <version>"; match the module
		// token.
		lhsFields := strings.Fields(lhs)
		if len(lhsFields) == 0 || lhsFields[0] != module {
			continue
		}
		// The RHS is "<target>" or "<target> <version>"; the target is its
		// first token.
		rhsFields := strings.Fields(rhs)
		if len(rhsFields) == 0 {
			continue
		}
		return filepath.FromSlash(rhsFields[0]), true
	}
	return "", false
}

// Extract parses every .proto file under protoDir (recursively) and returns
// one RPC per gRPC rpc, sorted by FQN for determinism.
//
// A protoDir that does not exist yields an empty result and no error — the
// graceful "no proto module resolved" path. A genuinely malformed .proto
// file is an error: a kacho-proto checkout that does not parse is a real
// defect the caller should surface.
func Extract(protoDir string) ([]RPC, error) {
	info, err := os.Stat(protoDir)
	if err != nil || !info.IsDir() {
		// No proto tree on disk — not an error, just an empty contract.
		return nil, nil
	}

	var files []string
	walkErr := filepath.WalkDir(protoDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".proto") {
			files = append(files, p)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan proto tree %s: %w", protoDir, walkErr)
	}
	sort.Strings(files)

	var out []RPC
	for _, f := range files {
		rpcs, err := extractFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, rpcs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FQN < out[j].FQN })
	return out, nil
}

// extractFile parses one .proto file and returns its gRPC RPCs.
func extractFile(path string) ([]RPC, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from a controlled WalkDir over protoDir.
	if err != nil {
		return nil, fmt.Errorf("open proto file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	got, err := protoparser.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse proto file %s: %w", path, err)
	}

	pkg := protoPackage(got)
	var out []RPC
	for _, body := range got.ProtoBody {
		svc, ok := body.(*parser.Service)
		if !ok {
			continue
		}
		out = append(out, extractService(pkg, svc)...)
	}
	return out, nil
}

// protoPackage returns the `package` declaration of a parsed proto file, or
// "" when the file declares none.
func protoPackage(p *parser.Proto) string {
	for _, body := range p.ProtoBody {
		if pkg, ok := body.(*parser.Package); ok {
			return pkg.Name
		}
	}
	return ""
}

// extractService returns one RPC per rpc declared in svc, with FQNs rooted
// at the proto package.
func extractService(pkg string, svc *parser.Service) []RPC {
	var out []RPC
	for _, body := range svc.ServiceBody {
		rpc, ok := body.(*parser.RPC)
		if !ok {
			continue
		}
		r := RPC{
			Service:  svc.ServiceName,
			Method:   rpc.RPCName,
			Request:  requestType(rpc.RPCRequest),
			Response: responseType(rpc.RPCResponse),
		}
		if pkg != "" {
			r.FQN = pkg + "." + svc.ServiceName + "/" + rpc.RPCName
		} else {
			r.FQN = svc.ServiceName + "/" + rpc.RPCName
		}
		r.RESTVerb, r.RESTPath = restAnnotation(rpc)
		out = append(out, r)
	}
	return out
}

// requestType returns the message type of an rpc request, or "" when the
// request is nil.
func requestType(req *parser.RPCRequest) string {
	if req == nil {
		return ""
	}
	return req.MessageType
}

// responseType returns the message type of an rpc response, or "" when the
// response is nil.
func responseType(resp *parser.RPCResponse) string {
	if resp == nil {
		return ""
	}
	return resp.MessageType
}

// restAnnotation extracts the REST verb and path from an rpc's
// (google.api.http) option. It returns ("", "") when the rpc carries no
// such option — every Internal-service method.
func restAnnotation(rpc *parser.RPC) (verb, path string) {
	for _, opt := range rpc.Options {
		if !isHTTPOption(opt.OptionName) {
			continue
		}
		return parseHTTPConstant(opt.Constant)
	}
	return "", ""
}

// isHTTPOption reports whether an option name denotes the google.api.http
// annotation. go-protoparser keeps the parenthesised extension form
// "(google.api.http)" verbatim.
func isHTTPOption(name string) bool {
	trimmed := strings.Trim(name, "()")
	return trimmed == "google.api.http"
}

// parseHTTPConstant reads the verb and path out of a (google.api.http)
// option's message-literal constant.
//
// go-protoparser normalises the literal to a brace-delimited,
// newline-separated key:value form, e.g.
//
//	{post:"/vpc/v1/networks"\nbody:"*"}
//	{get:"/vpc/v1/networks/{network_id}"}
//	{custom:{kind:"MOVE"\npath:"/vpc/v1/networks/{n}:move"}\nbody:"*"}
//
// The first key is the HTTP verb (get / post / put / patch / delete) or
// "custom"; its value is the path. For "custom" the verb is reported as
// "CUSTOM" and the path is the nested literal's "path" value. An
// unrecognised shape yields ("", "").
func parseHTTPConstant(constant string) (verb, path string) {
	body := strings.TrimSpace(constant)
	body = strings.TrimPrefix(body, "{")
	body = strings.TrimSuffix(body, "}")

	for _, key := range []string{"get", "post", "put", "patch", "delete"} {
		if p, ok := scalarValue(body, key); ok {
			return strings.ToUpper(key), p
		}
	}
	// The custom-verb form: custom:{kind:"…" path:"…"}.
	if inner, ok := nestedLiteral(body, "custom"); ok {
		p, _ := scalarValue(inner, "path")
		return "CUSTOM", p
	}
	return "", ""
}

// scalarValue returns the unquoted string value bound to key in a
// normalised message-literal body — a body of `key:"value"` entries
// separated by newlines. It reports ok=false when key is absent or its
// value is not a plain string scalar (e.g. a nested literal).
func scalarValue(body, key string) (string, bool) {
	for _, entry := range strings.Split(body, "\n") {
		entry = strings.TrimSpace(entry)
		rest, ok := strings.CutPrefix(entry, key+":")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, `"`) {
			return "", false
		}
		if v, err := strconv.Unquote(rest); err == nil {
			return v, true
		}
		return "", false
	}
	return "", false
}

// nestedLiteral returns the inner body of a nested message literal bound to
// key — `key:{…}` — with the surrounding braces stripped. It reports
// ok=false when key is absent or its value is not a brace literal.
func nestedLiteral(body, key string) (string, bool) {
	idx := indexOfKey(body, key)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(body[idx+len(key)+1:])
	if !strings.HasPrefix(rest, "{") {
		return "", false
	}
	depth := 0
	for i, r := range rest {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[1:i], true
			}
		}
	}
	return "", false
}

// indexOfKey returns the byte offset of an entry "key:" in a normalised
// message-literal body, considering only entries at the start of a
// newline-separated segment. It returns -1 when the key is absent.
func indexOfKey(body, key string) int {
	offset := 0
	for _, seg := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(seg, " \t")
		lead := len(seg) - len(trimmed)
		if strings.HasPrefix(trimmed, key+":") {
			return offset + lead
		}
		offset += len(seg) + 1 // +1 for the consumed '\n'
	}
	return -1
}
