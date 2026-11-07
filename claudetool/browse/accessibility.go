package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"shelley.exe.dev/llm"
)

func (b *BrowseTools) accessibilityHelp() llm.ToolOut {
	helpText := `Accessibility Tree Inspection — actions on the browser tool.

Actions (pass as the browser tool's "action"):

  accessibility_tree - Get the full accessibility tree
    Parameters:
      depth (int, optional): Maximum depth to retrieve. 0 or omitted = unlimited.
    Example: {"action": "accessibility_tree", "depth": 3}

  accessibility_query - Search for nodes by accessible name and/or role
    Parameters:
      name (string, optional): Accessible name to match.
      role (string, optional): ARIA role to match.
      At least one of name or role should be provided.
    Example: {"action": "accessibility_query", "role": "button"}
    Example: {"action": "accessibility_query", "name": "Submit"}

  accessibility_node - Get accessibility info for a specific DOM element
    Parameters:
      selector (string, required): CSS selector for the element.
    Example: {"action": "accessibility_node", "selector": "#login-button"}

  accessibility_help - Show this help text

Output format:
  tree: Indented tree showing [role] "name" (properties) for each node.
  query: Flat list of matching nodes with their properties.
  node: Detailed key-value pairs for a single element's accessibility info.`

	return llm.ToolOut{LLMContent: llm.TextContent(helpText)}
}

func (b *BrowseTools) accessibilityTree(ctx context.Context, timeout string, depth int) llm.ToolOut {
	var nodes []*accessibility.Node
	err := b.browserRun(ctx, timeout, "failed to get accessibility tree", chromedp.ActionFunc(func(ctx context.Context) error {
		params := accessibility.GetFullAXTree()
		if depth > 0 {
			params = params.WithDepth(int64(depth))
		}
		result, err := params.Do(ctx)
		if err != nil {
			return err
		}
		nodes = result
		return nil
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if len(nodes) == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent("No accessibility nodes found.")}
	}

	// Build map from nodeID to node
	nodeMap := make(map[accessibility.NodeID]*accessibility.Node, len(nodes))
	for _, n := range nodes {
		nodeMap[n.NodeID] = n
	}

	// Find root node (no parent)
	var root *accessibility.Node
	for _, n := range nodes {
		if n.ParentID == "" {
			root = n
			break
		}
	}
	if root == nil {
		// Fallback: use first node
		root = nodes[0]
	}

	var sb strings.Builder
	walkTree(&sb, root, nodeMap, 0)

	result := sb.String()
	return b.maybeWriteToFile(result, "ax_tree")
}

// walkTree recursively formats the accessibility tree.
func walkTree(sb *strings.Builder, node *accessibility.Node, nodeMap map[accessibility.NodeID]*accessibility.Node, depth int) {
	if node.Ignored {
		// Skip ignored nodes but recurse into their children at the same depth
		for _, childID := range node.ChildIDs {
			if child, ok := nodeMap[childID]; ok {
				walkTree(sb, child, nodeMap, depth)
			}
		}
		return
	}

	role := axValueStr(node.Role)
	name := axValueStr(node.Name)

	// Skip nodes with no useful role
	if role == "none" || role == "" {
		// Still recurse into children at same depth
		for _, childID := range node.ChildIDs {
			if child, ok := nodeMap[childID]; ok {
				walkTree(sb, child, nodeMap, depth)
			}
		}
		return
	}

	indent := strings.Repeat("  ", depth)

	// Format properties
	props := formatProperties(node.Properties)

	line := fmt.Sprintf("%s[%s]", indent, role)
	if name != "" {
		line += fmt.Sprintf(" %q", name)
	}
	if props != "" {
		line += fmt.Sprintf(" (%s)", props)
	}
	sb.WriteString(line)
	sb.WriteByte('\n')

	// Recurse into children
	for _, childID := range node.ChildIDs {
		if child, ok := nodeMap[childID]; ok {
			walkTree(sb, child, nodeMap, depth+1)
		}
	}
}

func (b *BrowseTools) accessibilityQuery(ctx context.Context, timeout, name, role string) llm.ToolOut {
	if name == "" && role == "" {
		return llm.ErrorfToolOut("at least one of 'name' or 'role' must be provided")
	}

	var nodes []*accessibility.Node
	err := b.browserRun(ctx, timeout, "failed to query accessibility tree", chromedp.ActionFunc(func(ctx context.Context) error {
		// Get the document root node ID
		doc, err := dom.GetDocument().WithDepth(0).Do(ctx)
		if err != nil {
			return fmt.Errorf("failed to get document: %w", err)
		}

		params := accessibility.QueryAXTree().WithNodeID(doc.NodeID)
		if name != "" {
			params = params.WithAccessibleName(name)
		}
		if role != "" {
			params = params.WithRole(role)
		}
		result, err := params.Do(ctx)
		if err != nil {
			return err
		}
		nodes = result
		return nil
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if len(nodes) == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent("No matching accessibility nodes found.")}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d node(s):\n", len(nodes))
	for _, node := range nodes {
		if node.Ignored {
			continue
		}
		roleStr := axValueStr(node.Role)
		nameStr := axValueStr(node.Name)
		props := formatProperties(node.Properties)

		line := fmt.Sprintf("[%s]", roleStr)
		if nameStr != "" {
			line += fmt.Sprintf(" %q", nameStr)
		}
		if props != "" {
			line += fmt.Sprintf(" (%s)", props)
		}
		if node.BackendDOMNodeID != 0 {
			line += fmt.Sprintf(" backendNodeId=%d", node.BackendDOMNodeID)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	result := sb.String()
	return b.maybeWriteToFile(result, "ax_query")
}

func (b *BrowseTools) accessibilityNode(ctx context.Context, timeout, selector string) llm.ToolOut {
	if selector == "" {
		return llm.ErrorfToolOut("'selector' parameter is required for the node action")
	}

	// Find the DOM node and get its AX tree in a single Run call
	var axNodes []*accessibility.Node
	err := b.browserRun(ctx, timeout, fmt.Sprintf("failed to get accessibility info for %q", selector),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var domNodes []*cdp.Node
			if err := chromedp.Nodes(selector, &domNodes, chromedp.ByQuery).Do(ctx); err != nil {
				return fmt.Errorf("failed to find element %q: %w", selector, err)
			}
			if len(domNodes) == 0 {
				return fmt.Errorf("no element found for selector %q", selector)
			}
			result, err := accessibility.GetPartialAXTree().
				WithBackendNodeID(domNodes[0].BackendNodeID).
				WithFetchRelatives(false).
				Do(ctx)
			if err != nil {
				return err
			}
			axNodes = result
			return nil
		}),
	)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Find the first non-ignored node
	var target *accessibility.Node
	for _, n := range axNodes {
		if !n.Ignored {
			target = n
			break
		}
	}
	if target == nil {
		if len(axNodes) > 0 {
			return llm.ToolOut{LLMContent: llm.TextContent("Element is ignored for accessibility.")}
		}
		return llm.ToolOut{LLMContent: llm.TextContent("No accessibility node found for this element.")}
	}

	// Format as detailed key-value pairs
	var sb strings.Builder
	fmt.Fprintf(&sb, "role: %s\n", axValueStr(target.Role))
	fmt.Fprintf(&sb, "name: %s\n", axValueStr(target.Name))
	if desc := axValueStr(target.Description); desc != "" {
		fmt.Fprintf(&sb, "description: %s\n", desc)
	}
	if val := axValueStr(target.Value); val != "" {
		fmt.Fprintf(&sb, "value: %s\n", val)
	}
	if target.BackendDOMNodeID != 0 {
		fmt.Fprintf(&sb, "backendDOMNodeId: %d\n", target.BackendDOMNodeID)
	}
	for _, prop := range target.Properties {
		propVal := axValueStr(prop.Value)
		if propVal != "" {
			fmt.Fprintf(&sb, "%s: %s\n", prop.Name, propVal)
		}
	}

	result := sb.String()
	return b.maybeWriteToFile(result, "ax_node")
}

// axValueStr extracts a string representation from an accessibility.Value.
func axValueStr(v *accessibility.Value) string {
	if v == nil {
		return ""
	}
	raw := v.Value.String()
	// Try to unquote JSON strings
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return s
	}
	return raw
}

// formatProperties formats AX properties into a compact string.
func formatProperties(properties []*accessibility.Property) string {
	// Boolean-ish properties: show name only when value is "true"
	boolProps := map[accessibility.PropertyName]bool{
		"focusable":       true,
		"disabled":        true,
		"editable":        true,
		"hidden":          true,
		"required":        true,
		"checked":         true,
		"expanded":        true,
		"selected":        true,
		"readonly":        true,
		"focused":         true,
		"modal":           true,
		"multiline":       true,
		"multiselectable": true,
		"settable":        true,
	}

	// Key=value properties: always show with their value
	kvProps := map[accessibility.PropertyName]bool{
		"level":        true,
		"autocomplete": true,
		"hasPopup":     true,
		"orientation":  true,
		"valuemin":     true,
		"valuemax":     true,
		"valuetext":    true,
	}

	var parts []string
	for _, prop := range properties {
		val := axValueStr(prop.Value)
		if boolProps[prop.Name] {
			if val == "true" {
				parts = append(parts, string(prop.Name))
			}
		} else if kvProps[prop.Name] {
			if val != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", prop.Name, val))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// maybeWriteToFile returns the text directly if small enough, or writes it to
// a file and returns the path.
func (b *BrowseTools) maybeWriteToFile(text, prefix string) llm.ToolOut {
	if len(text) <= ConsoleLogSizeThreshold {
		return llm.ToolOut{LLMContent: llm.TextContent(text)}
	}

	filename := fmt.Sprintf("%s_%s.txt", prefix, uuid.New().String()[:8])
	filePath := filepath.Join(ConsoleLogsDir, filename)
	if err := os.WriteFile(filePath, []byte(text), 0o644); err != nil {
		return llm.ErrorfToolOut("failed to write result to file: %w", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Output written to %s (%d bytes)", filePath, len(text)))}
}
