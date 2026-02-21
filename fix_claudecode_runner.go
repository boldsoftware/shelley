package main

import (
	"os"
	"strings"
)

func main() {
	b, _ := os.ReadFile("server/claudecode_runner.go")
	s := string(b)
	
	s = strings.ReplaceAll(s, "createdUserMsg.SequenceID", "(*createdUserMsg).SequenceID")
	s = strings.ReplaceAll(s, "createdMsg.SequenceID", "(*createdMsg).SequenceID")
	s = strings.ReplaceAll(s, "[]generated.Message{createdUserMsg}", "[]generated.Message{*createdUserMsg}")
	s = strings.ReplaceAll(s, "[]generated.Message{createdMsg}", "[]generated.Message{*createdMsg}")
	s = strings.ReplaceAll(s, "(*(*createdUserMsg))", "(*createdUserMsg)")
	s = strings.ReplaceAll(s, "(*(*createdMsg))", "(*createdMsg)")
	
	s = strings.ReplaceAll(s, "db.MessageTypeAssistant", "db.MessageTypeAgent")
	s = strings.ReplaceAll(s, "ToolID:    c.ID,", "ToolUseID: c.ID,")
	s = strings.ReplaceAll(s, "ToolID:     c.ToolUseID,", "ToolUseID: c.ToolUseID,")
	s = strings.ReplaceAll(s, "StreamResponse{Delta: &txt}", "StreamResponse{Delta: &txt, ConversationID: conversationID}")
	s = strings.ReplaceAll(s, "StreamResponse{ToolUse: &toolUse}", "StreamResponse{ToolUse: &toolUse, ConversationID: conversationID}")
	s = strings.ReplaceAll(s, "StreamResponse{ToolResult: &toolRes}", "StreamResponse{ToolResult: &toolRes, ConversationID: conversationID}")
	
	os.WriteFile("server/claudecode_runner.go", []byte(s), 0644)
}
