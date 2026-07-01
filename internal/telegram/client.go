// Package telegram is a minimal client for the Telegram Bot API, covering only
// what the expense bot needs: receiving webhook updates, downloading photos,
// sending replies, and registering the webhook on startup. It uses the standard
// library only (no third-party Telegram SDK).
package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the Telegram Bot API for a single bot token.
type Client struct {
	token string
	http  *http.Client
}

// NewClient builds a client for the given bot token (from @BotFather).
func NewClient(token string) *Client {
	return &Client{
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) apiURL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.token, method)
}

// ------------------------------------------------------------
// Update types (only the fields we consume)
// ------------------------------------------------------------

// Update is a single incoming update delivered to the webhook.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// CallbackQuery is delivered when a user taps an inline-keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"` // the message the keyboard is attached to
}

// Message is a chat message; it may carry text and/or photo sizes.
type Message struct {
	MessageID int         `json:"message_id"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	Photo     []PhotoSize `json:"photo"`
}

// Chat identifies the conversation; Chat.ID is what we map to an app user.
type Chat struct {
	ID int64 `json:"id"`
}

// PhotoSize is one resolution variant of an uploaded photo.
type PhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// LargestPhotoID returns the file_id of the highest-resolution photo in the
// message, or "" if the message has no photo. Telegram orders Photo by
// ascending size, so the last entry is the largest.
func (m *Message) LargestPhotoID() string {
	if len(m.Photo) == 0 {
		return ""
	}
	return m.Photo[len(m.Photo)-1].FileID
}

// ------------------------------------------------------------
// API methods
// ------------------------------------------------------------

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (c *Client) do(method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := c.http.Post(c.apiURL(method), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var parsed apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("failed to decode %s response: %v", method, err)
	}
	if !parsed.OK {
		return fmt.Errorf("telegram %s failed: %s", method, parsed.Description)
	}
	if result != nil && len(parsed.Result) > 0 {
		return json.Unmarshal(parsed.Result, result)
	}
	return nil
}

// SendMessage sends a plain-text reply to a chat.
func (c *Client) SendMessage(chatID int64, text string) error {
	return c.do("sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    text,
	}, nil)
}

// InlineKeyboardButton is one tappable button; CallbackData is echoed back in a
// CallbackQuery when tapped (max 64 bytes).
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// InlineKeyboardMarkup is a grid of inline buttons attached to a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// SendMessageWithKeyboard sends a message with an inline keyboard and returns
// the sent message's ID so it can be edited later.
func (c *Client) SendMessageWithKeyboard(chatID int64, text string, markup InlineKeyboardMarkup) (int, error) {
	var result struct {
		MessageID int `json:"message_id"`
	}
	err := c.do("sendMessage", map[string]any{
		"chat_id":      chatID,
		"text":         text,
		"reply_markup": markup,
	}, &result)
	return result.MessageID, err
}

// EditMessageText replaces the text and keyboard of an existing message.
func (c *Client) EditMessageText(chatID int64, messageID int, text string, markup InlineKeyboardMarkup) error {
	return c.do("editMessageText", map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"text":         text,
		"reply_markup": markup,
	}, nil)
}

// EditMessagePlain replaces the text of a message and removes its keyboard.
func (c *Client) EditMessagePlain(chatID int64, messageID int, text string) error {
	return c.do("editMessageText", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}, nil)
}

// AnswerCallbackQuery acknowledges a button tap (dismisses the client spinner).
func (c *Client) AnswerCallbackQuery(callbackID string) error {
	return c.do("answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
	}, nil)
}

// GetFile resolves a file_id to a downloadable file_path.
func (c *Client) GetFile(fileID string) (string, error) {
	var file struct {
		FilePath string `json:"file_path"`
	}
	if err := c.do("getFile", map[string]any{"file_id": fileID}, &file); err != nil {
		return "", err
	}
	if file.FilePath == "" {
		return "", fmt.Errorf("telegram returned empty file_path for %s", fileID)
	}
	return file.FilePath, nil
}

// DownloadFile fetches the bytes of a file_path and infers its media type from
// the extension (Telegram photos are JPEG).
func (c *Client) DownloadFile(filePath string) ([]byte, string, error) {
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.token, filePath)
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("failed to download file: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, mediaTypeFromPath(filePath), nil
}

func mediaTypeFromPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// SetWebhook registers the webhook URL with Telegram and sets the secret token
// that Telegram echoes back in the X-Telegram-Bot-Api-Secret-Token header.
func (c *Client) SetWebhook(url, secret string) error {
	payload := map[string]any{"url": url}
	if secret != "" {
		payload["secret_token"] = secret
	}
	return c.do("setWebhook", payload, nil)
}
