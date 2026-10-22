package agent

import (
	"fmt"
	"strings"
)

// ConversationItem 表示历史通讯的单轮记录
type ConversationItem struct {
	Role                     string // user, assistant, or tool
	Content                  string
	Parts                    []MessagePart
	ReasoningContent         string
	ToolCalls                []LLMToolCall
	ToolCallID               string
	ToolCallName             string
	ToolCallThoughtSignature string
}

const (
	MessagePartText  = "text"
	MessagePartImage = "image"
)

// Per-payload bounds. Providers enforce their own limits; these exist so a bug in
// a producer cannot turn into a runaway request that the gateway rejects only
// after the whole prompt has been billed.
const (
	maxMessageParts      = 32
	maxMessageImages     = 24
	maxMessageImageBytes = 5 << 20
	maxMessageImageSide  = 4096
)

var allowedMessageImageMIMEs = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/webp": {},
}

// MessagePart 是多模态载荷的一个单元。
//
// The runtime stays text-only unless a producer attaches parts: Content remains
// authoritative when Parts is empty, which keeps every existing request
// byte-identical and lets images arrive one call site at a time.
type MessagePart struct {
	Kind string // MessagePartText or MessagePartImage
	Text string
	MIME string // image/png, image/jpeg, image/webp
	Data []byte // only for image parts
	W, H int
}

func TextMessagePart(text string) MessagePart {
	return MessagePart{Kind: MessagePartText, Text: text}
}

func ImageMessagePart(mimeType string, data []byte, width, height int) MessagePart {
	return MessagePart{Kind: MessagePartImage, MIME: mimeType, Data: data, W: width, H: height}
}

// payloadParts returns the parts of one payload unit with the text first, so the
// model reads what the images belong to before it looks at them.
func payloadParts(text string, parts []MessagePart) []MessagePart {
	if len(parts) == 0 {
		return []MessagePart{TextMessagePart(text)}
	}
	if strings.TrimSpace(text) == "" {
		return parts
	}
	combined := make([]MessagePart, 0, len(parts)+1)
	combined = append(combined, TextMessagePart(text))
	return append(combined, parts...)
}

func validateMessageParts(parts []MessagePart) error {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) > maxMessageParts {
		return fmt.Errorf("too many message parts: %d > %d", len(parts), maxMessageParts)
	}
	images := 0
	for index, part := range parts {
		switch part.Kind {
		case MessagePartText:
			if part.Text == "" && len(part.Data) > 0 {
				return fmt.Errorf("part %d: text part carries binary data", index)
			}
		case MessagePartImage:
			images++
			if _, ok := allowedMessageImageMIMEs[part.MIME]; !ok {
				return fmt.Errorf("part %d: unsupported image MIME %q", index, part.MIME)
			}
			if len(part.Data) == 0 {
				return fmt.Errorf("part %d: image part has no data", index)
			}
			if len(part.Data) > maxMessageImageBytes {
				return fmt.Errorf("part %d: image is %d bytes, limit %d", index, len(part.Data), maxMessageImageBytes)
			}
			if part.W <= 0 || part.H <= 0 || part.W > maxMessageImageSide || part.H > maxMessageImageSide {
				return fmt.Errorf("part %d: invalid image dimensions %dx%d", index, part.W, part.H)
			}
		default:
			return fmt.Errorf("part %d: unknown kind %q", index, part.Kind)
		}
	}
	if images > maxMessageImages {
		return fmt.Errorf("too many images: %d > %d", images, maxMessageImages)
	}
	return nil
}

func validatePromptBundle(bundle *PromptBundle) error {
	if bundle == nil {
		return fmt.Errorf("prompt bundle is required")
	}
	for index, block := range bundle.RuntimeContext {
		if block.Name == "" || block.Name != strings.TrimSpace(block.Name) {
			return fmt.Errorf("runtime context block %d name must be a non-empty exact value", index)
		}
		if block.Role != LLMRoleUser {
			return fmt.Errorf("runtime context block %d must use the user transport role", index)
		}
		if err := validateMessageParts(block.Parts); err != nil {
			return fmt.Errorf("runtime context block %d: %w", index, err)
		}
	}
	for index, item := range bundle.History {
		switch item.Role {
		case LLMRoleUser, LLMRoleAssistant, LLMRoleTool:
		default:
			return fmt.Errorf("history item %d has unsupported role %q", index, item.Role)
		}
		if err := validateMessageParts(item.Parts); err != nil {
			return fmt.Errorf("history item %d: %w", index, err)
		}
	}
	return nil
}

// RuntimeContextBlock 表示在会话过程中因为摘要、事实等被动态注入的上下文
type RuntimeContextBlock struct {
	Name    string
	Role    string
	Content string
	// Parts 是叠加在 Content 之上的多模态载荷；为空时行为与纯文本完全一致。
	Parts []MessagePart
}

// PromptBundle 内部抽象的分层 Prompt 语义模型
type PromptBundle struct {
	Policy         string
	PolicyAppendix string
	Task           string
	RuntimeContext []RuntimeContextBlock
	History        []ConversationItem
}
