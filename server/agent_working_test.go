package server

import (
	"fmt"
	"testing"

	"shelley.exe.dev/db"
)

func TestAgentWorking(t *testing.T) {
	tests := []struct {
		name     string
		messages []APIMessage
		want     bool
	}{
		{
			name:     "empty messages",
			messages: []APIMessage{},
			want:     false,
		},
		{
			name: "agent with end_of_turn true",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: truePtr},
			},
			want: false,
		},
		{
			name: "agent with end_of_turn false",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: falsePtr},
			},
			want: true,
		},
		{
			name: "agent with end_of_turn nil",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: nil},
			},
			want: true,
		},
		{
			name: "error message",
			messages: []APIMessage{
				{Type: string(db.MessageTypeError)},
			},
			want: false,
		},
		{
			name: "agent end_of_turn then tool message means working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: truePtr},
				{Type: string(db.MessageTypeTool)},
			},
			want: true,
		},
		{
			name: "gitinfo after agent end_of_turn should NOT indicate working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: truePtr},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: false,
		},
		{
			name: "multiple gitinfo after agent end_of_turn should NOT indicate working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: truePtr},
				{Type: string(db.MessageTypeGitInfo)},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: false,
		},
		{
			name: "gitinfo after agent not end_of_turn should indicate working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: falsePtr},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: true,
		},
		{
			name: "only gitinfo messages",
			messages: []APIMessage{
				{Type: string(db.MessageTypeGitInfo)},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentWorking(tt.messages)
			if got == nil || *got != tt.want {
				gotVal := "nil"
				if got != nil {
					gotVal = fmt.Sprintf("%v", *got)
				}
				t.Errorf("agentWorking() = %v, want %v", gotVal, tt.want)
			}
		})
	}
}
