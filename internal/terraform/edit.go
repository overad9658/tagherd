package terraform

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Block names the block whose tags argument is edited: a resource, or the
// module call a resource's tags come in through.
type Block struct {
	Type   string // resource or module
	Labels []string
	Attr   string // tags, or labels for GCP
}

func (b Block) String() string {
	if b.Type == "module" {
		return "module." + strings.Join(b.Labels, ".")
	}
	return strings.Join(b.Labels, ".")
}

// Edit is one tag to set.
type Edit struct{ Key, Value string }

// EditResult is what happened to one edit.
type EditResult struct {
	Changed bool   // the code now sets the value
	Reason  string // set when the edit could not be made: a model.Unroutable* code
	Detail  string
}

// editFile sets each edit in the block's tags argument and returns the new
// source. found is false when the file has no such block. Edits are spliced
// in at the parsed byte ranges, so nothing else in the file moves; the file
// is reformatted only when it was already in canonical format.
func editFile(src []byte, name string, b Block, edits []Edit, sameKey func(a, b string) bool) (out []byte, res []EditResult, found bool, err error) {
	out = src
	for _, e := range edits {
		file, diags := hclsyntax.ParseConfig(out, name, hcl.InitialPos)
		if diags.HasErrors() {
			return src, nil, false, fmt.Errorf("parsing %s: %s", name, diags.Error())
		}
		blk := findBlock(file.Body.(*hclsyntax.Body), b)
		if blk == nil {
			return src, nil, false, nil
		}
		var r EditResult
		out, r = editBlock(out, blk, b, e, sameKey)
		res = append(res, r)
	}
	if formatted := hclwrite.Format(src); bytes.Equal(formatted, src) {
		out = hclwrite.Format(out)
	}
	return out, res, true, nil
}

func findBlock(body *hclsyntax.Body, b Block) *hclsyntax.Block {
	for _, blk := range body.Blocks {
		if blk.Type != b.Type || len(blk.Labels) != len(b.Labels) {
			continue
		}
		same := true
		for i, l := range blk.Labels {
			same = same && l == b.Labels[i]
		}
		if same {
			return blk
		}
	}
	return nil
}

func editBlock(src []byte, blk *hclsyntax.Block, b Block, e Edit, sameKey func(a, b string) bool) ([]byte, EditResult) {
	attr, ok := blk.Body.Attributes[b.Attr]
	if !ok {
		if b.Type == "module" {
			return src, unsupported("the call to %s passes no %s argument", b, b.Attr)
		}
		return addAttribute(src, blk, b.Attr, e), EditResult{Changed: true}
	}
	switch expr := attr.Expr.(type) {
	case *hclsyntax.ObjectConsExpr:
		return editObject(src, expr, b.Attr, e, sameKey)
	case *hclsyntax.FunctionCallExpr:
		if expr.Name != "merge" || len(expr.Args) == 0 || expr.ExpandFinal {
			break
		}
		if obj, ok := expr.Args[len(expr.Args)-1].(*hclsyntax.ObjectConsExpr); ok {
			return editObject(src, obj, b.Attr, e, sameKey)
		}
		// merge(local.common_tags, var.extra): a new last argument wins
		// over every earlier one.
		at := expr.Args[len(expr.Args)-1].Range().End.Byte
		return splice(src, at, at, ", { "+item(e)+" }"), EditResult{Changed: true}
	}
	return src, unsupported("%s = %s, which tagherd does not edit", b.Attr, text(src, attr.Expr.Range()))
}

// editObject sets e in an object constructor: the value of a matching key
// is replaced when it is a literal string, and a missing key is added as
// the last item.
func editObject(src []byte, obj *hclsyntax.ObjectConsExpr, attr string, e Edit, sameKey func(a, b string) bool) ([]byte, EditResult) {
	for _, it := range obj.Items {
		key, ok := objectKey(it.KeyExpr)
		if !ok || !sameKey(key, e.Key) {
			continue
		}
		v, lit := literalString(it.ValueExpr)
		switch {
		case !lit:
			return src, unsupported("%s.%s = %s, which tagherd does not edit", attr, key, text(src, it.ValueExpr.Range()))
		case v == e.Value:
			return src, EditResult{}
		}
		rng := it.ValueExpr.Range()
		return splice(src, rng.Start.Byte, rng.End.Byte, quote(e.Value)), EditResult{Changed: true}
	}

	closeAt := obj.SrcRange.End.Byte - 1 // the closing brace
	lineStart := bytes.LastIndexByte(src[:closeAt], '\n') + 1
	closeIndent := src[lineStart:closeAt]
	if obj.SrcRange.Start.Line != obj.SrcRange.End.Line && isBlank(closeIndent) {
		indent := string(closeIndent) + "  "
		if len(obj.Items) > 0 {
			indent = indentOf(src, obj.Items[0].KeyExpr.Range().Start.Byte)
		}
		return splice(src, lineStart, lineStart, indent+item(e)+"\n"), EditResult{Changed: true}
	}

	// A one-line object becomes one item per line.
	outer := indentOf(src, obj.SrcRange.Start.Byte)
	var b strings.Builder
	b.WriteString("{\n")
	for _, it := range obj.Items {
		b.WriteString(outer + "  " + text(src, it.KeyExpr.Range()) + " = " + text(src, it.ValueExpr.Range()) + "\n")
	}
	b.WriteString(outer + "  " + item(e) + "\n" + outer + "}")
	return splice(src, obj.SrcRange.Start.Byte, obj.SrcRange.End.Byte, b.String()), EditResult{Changed: true}
}

// addAttribute adds attr = { e } as the block's last line.
func addAttribute(src []byte, blk *hclsyntax.Block, attr string, e Edit) []byte {
	outer := indentOf(src, blk.TypeRange.Start.Byte)
	inner := outer + "  "
	text := inner + attr + " = {\n" + inner + "  " + item(e) + "\n" + inner + "}\n"
	closeAt := blk.CloseBraceRange.Start.Byte
	lineStart := bytes.LastIndexByte(src[:closeAt], '\n') + 1
	if lineStart > blk.OpenBraceRange.Start.Byte && isBlank(src[lineStart:closeAt]) {
		return splice(src, lineStart, lineStart, text)
	}
	return splice(src, closeAt, closeAt, "\n"+text+outer)
}

func unsupported(format string, args ...any) EditResult {
	return EditResult{Reason: unsupportedReason, Detail: fmt.Sprintf(format, args...)}
}

// objectKey is an object item's key when it is a literal.
func objectKey(expr hclsyntax.Expression) (string, bool) {
	if k, ok := expr.(*hclsyntax.ObjectConsKeyExpr); ok {
		if k.ForceNonLiteral {
			return "", false
		}
		if st, ok := k.Wrapped.(*hclsyntax.ScopeTraversalExpr); ok && len(st.Traversal) == 1 {
			return st.Traversal.RootName(), true
		}
		expr = k.Wrapped
	}
	return literalString(expr)
}

// literalString is the value of a quoted string with no interpolation.
func literalString(expr hclsyntax.Expression) (string, bool) {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		if e.Val.Type() == cty.String && e.Val.IsKnown() && !e.Val.IsNull() {
			return e.Val.AsString(), true
		}
	case *hclsyntax.TemplateExpr:
		var b strings.Builder
		for _, p := range e.Parts {
			lit, ok := p.(*hclsyntax.LiteralValueExpr)
			if !ok || lit.Val.Type() != cty.String {
				return "", false
			}
			b.WriteString(lit.Val.AsString())
		}
		return b.String(), true
	}
	return "", false
}

func item(e Edit) string {
	key := e.Key
	if !hclsyntax.ValidIdentifier(key) {
		key = quote(key)
	}
	return key + " = " + quote(e.Value)
}

func quote(s string) string { return string(hclwrite.TokensForValue(cty.StringVal(s)).Bytes()) }

func text(src []byte, r hcl.Range) string { return string(src[r.Start.Byte:r.End.Byte]) }

func splice(src []byte, from, to int, s string) []byte {
	out := make([]byte, 0, len(src)+len(s))
	out = append(out, src[:from]...)
	out = append(out, s...)
	return append(out, src[to:]...)
}

// indentOf is the leading whitespace of the line holding offset.
func indentOf(src []byte, offset int) string {
	start := bytes.LastIndexByte(src[:offset], '\n') + 1
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}

func isBlank(b []byte) bool { return len(bytes.Trim(b, " \t")) == 0 }
