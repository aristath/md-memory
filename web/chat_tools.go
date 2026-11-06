package web

import (
	"encoding/json"
	"strings"
)

const toolRepair = "Your last tool call was incomplete. No tool in that reply was run. Call the tool again with every argument inside its JSON. Use description for entity/topic words and text for memory/story words. Do not write XML tool tags. Do not say the change was saved."

const failedWriteReminder = "The tool replies immediately before this note include a save that did not report a completed change. Do not say that part was saved until a later tool result confirms it. Read the error and fix the arguments, or check the operation receipt if work is pending. If you cannot finish, tell the person which part was not saved."

func failedToolResult(content string) bool {
	var result struct {
		IsError bool   `json:"isError"`
		Error   string `json:"error"`
	}
	return json.Unmarshal([]byte(content), &result) == nil && (result.IsError || result.Error != "")
}

// Retain malformed output as evidence. Never guess missing arguments or run XML.
func malformedToolReply(m chatMessage) bool {
	text := strings.TrimSpace(m.Content)
	if len(m.ToolCalls) == 0 && !strings.HasPrefix(text, "<tool_call>") && !strings.HasPrefix(text, "<function=") && !strings.HasPrefix(text, "<parameter=") {
		return false
	}
	for _, marker := range []string{"<tool_call>", "</tool_call>", "<function=", "</function>", "<parameter=", "</parameter>"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func originalTurnText(c chatConversation) string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Role == "user" && m.TurnID == c.TurnID {
			return m.Content
		}
	}
	return ""
}

func sameArguments(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

// Only consecutive identical failed/no-op writes in this turn count. A different tool
// or a real change allows useful work to continue. Accepted receipts stay intact.
func unproductiveRepeats(c chatConversation, index int, call *chatToolCall) int {
	if !strings.HasPrefix(call.Function.Name, "save_") {
		return 0
	}
	count := 0
	for i := index - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.TurnID != c.TurnID || m.Role == "user" {
			break
		}
		if m.Role != "tool" {
			continue
		}
		var result struct {
			Outcome    string `json:"outcome"`
			Error      string `json:"error"`
			IsError    bool   `json:"isError"`
			Structured struct {
				Error string `json:"error"`
				Data  struct {
					Outcome string `json:"outcome"`
				} `json:"data"`
			} `json:"structuredContent"`
		}
		if json.Unmarshal([]byte(m.Content), &result) != nil || m.Name != call.Function.Name {
			break
		}
		if result.Outcome != "unchanged" && result.Structured.Data.Outcome != "unchanged" && !result.IsError && result.Error == "" && result.Structured.Error == "" {
			break
		}
		matched := false
		for j := i - 1; j >= 0 && c.Messages[j].Role != "user"; j-- {
			for _, previous := range c.Messages[j].ToolCalls {
				if previous.ID == m.ToolCallID {
					matched = sameArguments(previous.Function.Arguments, call.Function.Arguments)
				}
			}
			if c.Messages[j].Role == "assistant" {
				break
			}
		}
		if !matched {
			break
		}
		count++
	}
	return count
}
