package app

import (
	"strings"

	"golang.org/x/net/html"
)

type providerTextUserAgentKey struct{}
type providerTextNoCacheKey struct{}
type providerTextSingleAttemptKey struct{}

func providerHTMLAttr(node *html.Node, name string) string {
	if node != nil {
		for _, attribute := range node.Attr {
			if attribute.Key == name {
				return attribute.Val
			}
		}
	}
	return ""
}

func providerHTMLClass(node *html.Node, name string) bool {
	for _, value := range strings.Fields(providerHTMLAttr(node, "class")) {
		if value == name {
			return true
		}
	}
	return false
}

func providerHTMLNodes(root *html.Node, match func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	if root == nil {
		return found
	}
	pending := []*html.Node{root}
	for len(pending) > 0 {
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if match(node) {
			found = append(found, node)
		}
		for child := node.LastChild; child != nil; child = child.PrevSibling {
			pending = append(pending, child)
		}
	}
	return found
}

func providerHTMLFirstClass(root *html.Node, names ...string) *html.Node {
	for _, name := range names {
		matches := providerHTMLNodes(root, func(node *html.Node) bool {
			return providerHTMLClass(node, name)
		})
		if len(matches) > 0 {
			return matches[0]
		}
	}
	return nil
}

func providerHTMLText(root *html.Node) string {
	var text strings.Builder
	for _, node := range providerHTMLNodes(root, func(node *html.Node) bool {
		return node.Type == html.TextNode
	}) {
		if node.Parent != nil && (node.Parent.Data == "script" || node.Parent.Data == "style") {
			continue
		}
		text.WriteString(node.Data)
		text.WriteByte(' ')
	}
	return strings.Join(strings.Fields(text.String()), " ")
}
