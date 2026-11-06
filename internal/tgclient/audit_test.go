package tgclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	pathTelegram = "github.com/gotd/td/telegram"
	pathTG       = "github.com/gotd/td/tg"
	pathAuth     = "github.com/gotd/td/telegram/auth"
	pathGuard    = "github.com/iNViTiON/telegram-go-mcp/internal/guard"
)

// sibling invokers that would bypass the guard.
var bypassMethods = map[string]bool{
	"Pool": true, "DC": true, "QR": true, "CDN": true, "MediaOnly": true,
}

type srcFile struct {
	rel     string // slash path relative to module root
	file    *ast.File
	imports map[string]string // import path -> local name
}

func loadModule(t *testing.T) []srcFile {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var out []srcFile
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		sf := srcFile{rel: filepath.ToSlash(rel), file: f, imports: map[string]string{}}
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			name := filepath.Base(ip)
			if im.Name != nil {
				name = im.Name.Name
			}
			sf.imports[ip] = name
		}
		out = append(out, sf)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no source files found; audit walked the wrong directory")
	}
	return out
}

// isPkgCall reports whether call is pkgPath.fn(...) using the file's own alias.
func (s srcFile) isPkgCall(call *ast.CallExpr, pkgPath, fn string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != fn {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == s.imports[pkgPath] && s.imports[pkgPath] != ""
}

func TestOnlyOneTelegramClientConstructor(t *testing.T) {
	var sites []string
	for _, sf := range loadModule(t) {
		ast.Inspect(sf.file, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sf.isPkgCall(c, pathTelegram, "NewClient") ||
					sf.isPkgCall(c, pathTelegram, "ClientFromEnvironment") {
					sites = append(sites, sf.rel)
				}
			}
			return true
		})
	}
	if len(sites) != 1 || sites[0] != "internal/tgclient/client.go" {
		t.Fatalf("want exactly one telegram client constructor, in internal/tgclient/client.go; got %v", sites)
	}
}

func TestNoSiblingInvokers(t *testing.T) {
	for _, sf := range loadModule(t) {
		ast.Inspect(sf.file, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sf.isPkgCall(c, pathTG, "NewClient") {
				t.Errorf("%s: tg.NewClient builds an invoker that bypasses the guard", sf.rel)
			}
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && bypassMethods[sel.Sel.Name] {
				t.Errorf("%s: .%s() creates a sibling connection that bypasses the guard", sf.rel, sel.Sel.Name)
			}
			return true
		})
	}
}

func TestClientOptionsAreGuarded(t *testing.T) {
	for _, sf := range loadModule(t) {
		if sf.rel != "internal/tgclient/client.go" {
			continue
		}
		var sawMiddlewares, sawNoUpdates bool
		ast.Inspect(sf.file, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return true
			}
			switch key.Name {
			case "NoUpdates":
				if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "true" {
					sawNoUpdates = true
				}
			case "Middlewares":
				lit, ok := kv.Value.(*ast.CompositeLit)
				if !ok || len(lit.Elts) == 0 {
					return true
				}
				first, ok := lit.Elts[0].(*ast.CallExpr)
				if ok && sf.isPkgCall(first, pathGuard, "Middleware") {
					sawMiddlewares = true
				}
			}
			return true
		})
		if !sawMiddlewares {
			t.Error("Options.Middlewares must start with guard.Middleware(...)")
		}
		if !sawNoUpdates {
			t.Error("Options.NoUpdates must be set to true explicitly")
		}
		return
	}
	t.Fatal("internal/tgclient/client.go not found")
}

// authEntryPoints are tgclient methods that reach auth.* RPCs. They are fine in
// the login binary and forbidden in the server binary. The guard also denies
// them at runtime under PolicyRead; this test keeps the intent visible.
var authEntryPoints = map[string]bool{"SignIn": true, "Auth": true}

func TestServerBinaryDoesNotUseAuth(t *testing.T) {
	for _, sf := range loadModule(t) {
		if !strings.HasPrefix(sf.rel, "cmd/telegram-mcp/") {
			continue
		}
		if _, bad := sf.imports[pathAuth]; bad {
			t.Errorf("%s imports %s; auth RPCs must stay out of the server binary", sf.rel, pathAuth)
		}
		ast.Inspect(sf.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && authEntryPoints[sel.Sel.Name] {
				t.Errorf("%s: .%s reaches auth RPCs; only cmd/telegram-mcp-login may use it", sf.rel, sel.Sel.Name)
			}
			return true
		})
	}
}
