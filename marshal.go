package xmltree

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"text/template"
)

// NOTE(droyo) As of go1.5.1, the encoding/xml package does not resolve
// prefixes in attribute names. Therefore we add .Name.Space verbatim
// instead of trying to resolve it. One consequence is this is that we cannot
// rename prefixes without some work.
var tagTmpl = template.Must(template.New("Marshal XML tags").Parse(
	`{{define "start" -}}
	<{{.Scope.Prefix .Name -}}
	{{range .StartElement.Attr}} {{$.Scope.Prefix .Name -}}="{{.Value}}"{{end -}}
	{{range .NS }} xmlns{{ if .Local }}:{{ .Local }}{{end}}="{{ .Space }}"{{end -}}
	{{if or .Children .Content}}>{{else}} />{{end}}
	{{- end}}

	{{define "end" -}}
	</{{.Prefix .Name}}>{{end}}`))

type vContentMapping struct {
	Decoded string
	Encoded string
}

var vContentMappings = []vContentMapping{
	{Decoded: `&`, Encoded: `&amp;`},
	{Decoded: `<`, Encoded: `&lt;`},
	{Decoded: `>`, Encoded: `&gt;`},
	{Decoded: `"`, Encoded: `&quot;`},
}

// XML encode any special characters in a plain string.
// For example & will be encoded as &amp;

func xmlEncodeString(strToEncode string) (string, error) {
	strEncoded := strToEncode

	for _, mapping := range vContentMappings {
		strEncoded = strings.Replace(strEncoded, mapping.Decoded, mapping.Encoded, -1)
	}

	//fmt.Printf("xmlEncodeString([%s]) -> [%s]\n", strToEncode, strEncoded)

	return strEncoded, nil
}

// XML decode escaped characters in a string.
// For example &quot; will be encoded as "

func xmlDecodeString(strToDecode string) (string, error) {
	strDecoded := strToDecode

	for _, mapping := range vContentMappings {
		strDecoded = strings.Replace(strDecoded, mapping.Encoded, mapping.Decoded, -1)
	}

	//fmt.Printf("xmlDecodeString([%s]) -> [%s]\n", strToDecode, strDecoded)

	return strDecoded, nil
}

// Marshal produces the XML encoding of an Element as a self-contained
// document. The xmltree package may adjust the declarations of XML
// namespaces if the Element has been modified, or is part of a larger scope,
// such that the document produced by Marshal is a valid XML document.
//
// The return value of Marshal will use the utf-8 encoding regardless of
// the original encoding of the source document.
func Marshal(el *Element) []byte {
	var buf bytes.Buffer
	if err := Encode(&buf, el); err != nil {
		// bytes.Buffer.Write should never return an error
		panic(err)
	}
	return buf.Bytes()
}

// MarshalIndent is like Marshal, but adds line breaks for each
// successive element. Each line begins with prefix and is
// followed by zero or more copies of indent according to the
// nesting depth.
func MarshalIndent(el *Element, prefix, indent string) []byte {
	var buf bytes.Buffer
	enc := encoder{
		w:      &buf,
		prefix: prefix,
		indent: indent,
		pretty: true,
	}
	if err := enc.encode(el, nil, make(map[*Element]struct{})); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// Encode writes the XML encoding of the Element to w.
// Encode returns any errors encountered writing to w.
func Encode(w io.Writer, el *Element) error {
	enc := encoder{w: w}
	return enc.encode(el, nil, make(map[*Element]struct{}))
}

// String returns the XML encoding of an Element
// and its children as a string.
func (el *Element) String() string {
	return string(Marshal(el))
}

type encoder struct {
	w              io.Writer
	prefix, indent string
	pretty         bool
}

// This could be used to print a subset of an XML document, or a document
// that has been modified. In such an event, namespace declarations must
// be "pulled" in, so they can be resolved properly. This is trickier than
// just defining everything at the top level because there may be conflicts
// introduced by the modifications.
func (e *encoder) encode(el, parent *Element, visited map[*Element]struct{}) error {
	if len(visited) > recursionLimit {
		// We only return I/O errors
		return nil
	}
	if _, ok := visited[el]; ok {
		// We have a cycle. Leave a comment, but no error
		e.w.Write([]byte("<!-- cycle detected -->"))
		return nil
	}
	scope := diffScope(parent, el)
	if err := e.encodeOpenTag(el, scope, len(visited)); err != nil {
		return err
	}
	if len(el.Children) == 0 {
		if len(el.Content) > 0 {
			mStr, mErr := xmlEncodeString(string(el.Content))
			if mErr != nil {
				return mErr
			}
			e.w.Write([]byte(mStr))
		} else {
			return nil
		}
	}
	for i := range el.Children {
		visited[el] = struct{}{}
		if err := e.encode(&el.Children[i], el, visited); err != nil {
			return err
		}
		delete(visited, el)
	}
	if err := e.encodeCloseTag(el, len(visited)); err != nil {
		return err
	}
	return nil
}

// diffScope returns the Scope of the child element, minus any
// identical namespace declaration in the parent's scope.
func diffScope(parent, child *Element) Scope {
	if parent == nil { // root element
		return child.Scope
	}
	childScope := child.Scope
	parentScope := parent.Scope
	for len(parentScope.ns) > 0 && len(childScope.ns) > 0 {
		if childScope.ns[0] == parentScope.ns[0] {
			childScope.ns = childScope.ns[1:]
			parentScope.ns = parentScope.ns[1:]
		} else {
			break
		}
	}
	return childScope
}

func (e *encoder) encodeOpenTag(el *Element, scope Scope, depth int) error {
	if e.pretty {
		for i := 0; i < depth; i++ {
			io.WriteString(e.w, e.indent)
		}
	}
	// Note that a copy of el is used here so that XML encoded attributes are generated
	var elCopy *Element = &Element{}
	elCopy.StartElement = xml.StartElement{}
	elCopy.StartElement.Name = el.StartElement.Name
	elCopy.StartElement.Attr = make([]xml.Attr, len(el.StartElement.Attr))
	for i := 0; i < len(el.StartElement.Attr); i++ {
		elCopy.StartElement.Attr[i] = el.StartElement.Attr[i]
	}
	elCopy.Scope = el.Scope
	// Escape node contents
	{
		mStr, mErr := xmlEncodeString(string(el.Content))
		if mErr != nil {
			return mErr
		}
		elCopy.Content = []byte(mStr)
	}
	elCopy.Children = el.Children

	var tag = struct {
		*Element
		NS []xml.Name
	}{Element: elCopy, NS: scope.ns}

	// XML escape attribute strings held in copy
	attrs := tag.StartElement.Attr
	for i := 0; i < len(attrs); i++ {
		attrStr := attrs[i].Value
		mStr, mErr := xmlEncodeString(attrStr)
		if mErr != nil {
			return mErr
		}
		attrs[i].Value = mStr
	}
	tag.StartElement.Attr = attrs

	if err := tagTmpl.ExecuteTemplate(e.w, "start", tag); err != nil {
		return err
	}
	if e.pretty {
		if len(el.Children) > 0 || len(el.Content) == 0 {
			io.WriteString(e.w, "\n")
		}
	}
	return nil
}

func (e *encoder) encodeCloseTag(el *Element, depth int) error {
	if e.pretty {
		for i := 0; i < depth; i++ {
			if len(el.Children) > 0 {
				io.WriteString(e.w, e.indent)
			}
		}
	}
	if err := tagTmpl.ExecuteTemplate(e.w, "end", el); err != nil {
		return err
	}
	if e.pretty {
		io.WriteString(e.w, "\n")
	}
	return nil
}
