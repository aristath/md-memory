package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aristath/songya/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
)

func messageParam(m chatMessage) openai.ChatCompletionMessageParamUnion {
	value := map[string]any{"role": m.Role, "content": m.Content}
	if len(m.ToolCalls) > 0 {
		value["tool_calls"] = m.ToolCalls
	}
	if m.ToolCallID != "" {
		value["tool_call_id"] = m.ToolCallID
	}
	data, _ := json.Marshal(value)
	return param.Override[openai.ChatCompletionMessageParamUnion](json.RawMessage(data))
}

func pendingChatTool(c chatConversation) (int, *chatToolCall) {
	for i, m := range c.Messages {
		if m.Role != "assistant" {
			continue
		}
		for _, call := range m.ToolCalls {
			found := false
			for _, later := range c.Messages[i+1:] {
				if later.Role == "assistant" || later.Role == "user" {
					break
				}
				found = found || later.ToolCallID == call.ID
			}
			if !found {
				return i, &call
			}
		}
	}
	return 0, nil
}

func defaultChatOwner(tool *mcp.Tool, args map[string]any, user string) {
	if _, supplied := args["user"]; supplied {
		return
	}
	if (tool.Name == "save_memory" || tool.Name == "save_story") && args["id"] != nil {
		return
	}
	data, _ := json.Marshal(tool.InputSchema)
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(data, &schema)
	if _, ok := schema.Properties["user"]; ok {
		args["user"] = user
	}
}

func (a *chatApp) run(ctx context.Context, id string) error {
	ct, st := mcp.NewInMemoryTransports()
	server := mcpserver.New(ctx, a.store, nil)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		return err
	}
	defer ss.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "songya-chat", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	tools := map[string]*mcp.Tool{}
	definitions := []openai.ChatCompletionToolUnionParam{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		tools[tool.Name] = tool
		data, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
		definitions = append(definitions, param.Override[openai.ChatCompletionToolUnionParam](json.RawMessage(data)))
	}
	for requests := 0; requests < 100; {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := a.conversation(id)
		if err != nil {
			return err
		}
		if c.Status != "running" {
			return nil
		}
		c, err = a.prepareRecall(ctx, id, c)
		if err != nil {
			return err
		}
		if index, call := pendingChatTool(c); call != nil {
			var result any
			args := map[string]any{}
			decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
			decoder.UseNumber()
			tool := tools[call.Function.Name]
			if err := decoder.Decode(&args); err != nil || args == nil {
				result = map[string]any{"error": "Arguments must be a JSON object; include the information to read or save."}
			} else if tool == nil {
				result = map[string]any{"error": "Unknown Songya tool; use the supplied definitions."}
			} else {
				defaultChatOwner(tool, args, c.User)
				key := fmt.Sprintf("chat:%s:%d:%s", id, index, call.ID)
				result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: call.Function.Name, Arguments: args, Meta: mcp.Meta{"songya/request-key": key}})
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					result = map[string]any{"error": err.Error()}
				}
			}
			data, err := json.Marshal(result)
			if err != nil {
				return err
			}
			_, err = a.edit(a.ctx, id, func(current *chatConversation) error {
				// The result is retained even if the user paused during execution.
				if _, pending := pendingChatTool(*current); pending == nil {
					return nil
				}
				current.Messages = append(current.Messages, chatMessage{Role: "tool", Content: string(data), ToolCallID: call.ID, Name: call.Function.Name, TurnID: c.TurnID})
				return nil
			})
			if err != nil {
				return err
			}
			continue
		}
		s, err := a.settings()
		if err != nil {
			return err
		}
		if s.BaseURL == "" || s.Model == "" {
			return fmt.Errorf("Configure the API base URL and model in Chat settings, then retry the saved turn.")
		}
		system := c.Prompt + "\n\n# Connected Songya MCP guide\n" + session.InitializeResult().Instructions + fmt.Sprintf("\n\nThe person currently talking is user %q. This identifies ownership, not an entity.\n", c.User)
		messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system)}
		if evidence := recallContext(c); evidence != "" {
			messages = append(messages, openai.UserMessage(evidence))
		}
		for _, m := range c.Messages {
			messages = append(messages, messageParam(m))
		}
		client := s.client()
		requests++
		streamCtx, cancelStream := context.WithCancel(ctx)
		stream := client.Chat.Completions.NewStreaming(streamCtx, openai.ChatCompletionNewParams{Model: s.Model, Messages: messages, Tools: definitions})
		chunks := make(chan openai.ChatCompletionChunk, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			defer close(chunks)
			defer stream.Close()
			for stream.Next() {
				select {
				case chunks <- stream.Current():
				case <-streamCtx.Done():
					return
				}
			}
		}()
		acc := openai.ChatCompletionAccumulator{}
		ticker := time.NewTicker(150 * time.Millisecond)
		var draft chatMessage
		dirty := false
	streaming:
		for {
			select {
			case <-ctx.Done():
				err = ctx.Err()
				break streaming
			case chunk, ok := <-chunks:
				if !ok {
					break streaming
				}
				acc.AddChunk(chunk)
				if len(acc.Choices) == 0 {
					continue
				}
				m := acc.Choices[0].Message
				draft = chatMessage{Role: "assistant", Content: m.Content, TurnID: c.TurnID}
				for _, call := range m.ToolCalls {
					converted := chatToolCall{ID: call.ID, Type: "function"}
					converted.Function.Name = call.Function.Name
					converted.Function.Arguments = call.Function.Arguments
					draft.ToolCalls = append(draft.ToolCalls, converted)
				}
				dirty = true
			case <-ticker.C:
				if !dirty {
					continue
				}
				_, err = a.edit(a.ctx, id, func(current *chatConversation) error { current.Draft = &draft; return nil })
				if err != nil {
					break streaming
				}
				dirty = false
			}
		}
		ticker.Stop()
		cancelStream()
		<-finished
		if err == nil {
			err = stream.Err()
		}
		if err == nil && len(acc.Choices) == 0 {
			err = fmt.Errorf("The model returned no answer. The turn is saved; retry to continue.")
		}
		if err == nil && len(acc.Choices) > 0 {
			reason := acc.Choices[0].FinishReason
			if reason == "" {
				err = fmt.Errorf("The model stream ended before completion. The partial reply is retained; retry to continue.")
			}
			if reason == "length" {
				err = fmt.Errorf("The model reached its output limit. The partial reply is retained; retry to continue.")
			}
			if reason == "content_filter" {
				err = fmt.Errorf("The provider stopped the reply. The partial reply is retained.")
			}
		}
		if err != nil {
			if len(acc.Choices) > 0 {
				_, saveErr := a.edit(a.ctx, id, func(current *chatConversation) error { current.Draft = &draft; return nil })
				if saveErr != nil {
					return saveErr
				}
			}
			return fmt.Errorf("%s", safeChatError(err, s.APIKey))
		}
		seen := map[string]bool{}
		for _, previous := range c.Messages {
			for _, call := range previous.ToolCalls {
				seen[call.ID] = true
			}
		}
		for i := range draft.ToolCalls {
			call := &draft.ToolCalls[i]
			if call.ID == "" || seen[call.ID] {
				call.ID = fmt.Sprintf("call_%d_%d", len(c.Messages), i)
			}
			seen[call.ID] = true
		}
		_, err = a.edit(a.ctx, id, func(current *chatConversation) error {
			if current.Status != "running" || ctx.Err() != nil {
				return context.Canceled
			}
			current.Messages = append(current.Messages, draft)
			current.Draft = nil
			if len(draft.ToolCalls) == 0 {
				current.Status = "done"
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(draft.ToolCalls) == 0 {
			return nil
		}
	}
	return fmt.Errorf("The model has made 100 consecutive tool requests. Work and tool receipts are saved; retry to continue.")
}
