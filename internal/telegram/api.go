// Package telegram is the Telegram channel. It talks to the raw Bot API over
// net/http, with no Telegram library.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Bot API root.
const DefaultBaseURL = "https://api.telegram.org"

const (
	// maxRetries429 is the number of retries after a rate-limit reply.
	maxRetries429 = 3
	// callTimeout limits a JSON call when the context has no deadline.
	callTimeout = 60 * time.Second
	// uploadTimeout limits an upload or a download when the context has no deadline.
	uploadTimeout = 5 * time.Minute
	// generalTopicID is the thread id of the forum General topic.
	generalTopicID = "1"
)

// defaultHC is the one shared HTTP client. It has no global timeout, because
// each request sets its own deadline (the long poll needs more than 60 s).
var defaultHC = &http.Client{}

// Client calls the Telegram Bot API.
type Client struct {
	Token   string
	BaseURL string
	HC      *http.Client
	Log     *slog.Logger

	// sleep waits between rate-limit retries. Tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

// APIError is an "ok": false reply from the Bot API.
type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram API error %d: %s", e.Code, e.Description)
}

// Update is one item from getUpdates.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
	// EditedMessage is the new version of a message that the user changed.
	// Poller.Run also puts it in Message, so the handler treats it as a new
	// message (Hermes).
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is a Telegram message, with only the fields that Tether Agent uses.
type Message struct {
	MessageID       int             `json:"message_id"`
	MessageThreadID int             `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool            `json:"is_topic_message,omitempty"`
	From            *User           `json:"from,omitempty"`
	Chat            Chat            `json:"chat"`
	Date            int64           `json:"date,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
	MediaGroupID    string          `json:"media_group_id,omitempty"`
	Text            string          `json:"text,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	Caption         string          `json:"caption,omitempty"`
	CaptionEntities []MessageEntity `json:"caption_entities,omitempty"`
	Photo           []PhotoSize     `json:"photo,omitempty"`
	Document        *Document       `json:"document,omitempty"`
	Voice           *Voice          `json:"voice,omitempty"`
}

// Chat is the chat of a message.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type,omitempty"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	IsForum   bool   `json:"is_forum,omitempty"`
}

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// MessageEntity marks a part of the text, for example a mention or a command.
// Offset and Length count UTF-16 code units.
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	User   *User  `json:"user,omitempty"`
}

// PhotoSize is one size of a photo.
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Document is a general file.
type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Voice is a voice note.
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// CallbackQuery is a press on an inline keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// InlineKeyboardMarkup is a keyboard under a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// InlineKeyboardButton is one button of an inline keyboard.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// BotCommand is one entry of the bot command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// File is the reply of getFile.
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// ReplyParameters makes a message a reply to another message.
type ReplyParameters struct {
	MessageID                int  `json:"message_id"`
	AllowSendingWithoutReply bool `json:"allow_sending_without_reply,omitempty"`
}

// SendMessageParams are the sendMessage parameters. A zero MessageThreadID is
// left out. Use ThreadParam to set it.
type SendMessageParams struct {
	ChatID          int64                 `json:"chat_id"`
	MessageThreadID int                   `json:"message_thread_id,omitempty"`
	Text            string                `json:"text"`
	ParseMode       string                `json:"parse_mode,omitempty"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	ReplyParameters *ReplyParameters      `json:"reply_parameters,omitempty"`
}

// InputRichMessage is a rich message to send. Tether Agent uses only the
// Markdown form, which is GitHub Flavored Markdown.
type InputRichMessage struct {
	Markdown string `json:"markdown"`
}

// SendRichMessageParams are the sendRichMessage parameters. A zero
// MessageThreadID is left out. Use ThreadParam to set it.
type SendRichMessageParams struct {
	ChatID          int64                 `json:"chat_id"`
	MessageThreadID int                   `json:"message_thread_id,omitempty"`
	RichMessage     InputRichMessage      `json:"rich_message"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageTextParams are the editMessageText parameters. Set Text for a
// plain edit, or RichMessage for a rich edit. The empty one is left out.
type EditMessageTextParams struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int                   `json:"message_id"`
	Text        string                `json:"text,omitempty"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	RichMessage *InputRichMessage     `json:"rich_message,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SendMessageDraftParams are the sendMessageDraft parameters. DraftID must not
// be zero. The same DraftID updates the same draft.
type SendMessageDraftParams struct {
	ChatID          int64  `json:"chat_id"`
	MessageThreadID int    `json:"message_thread_id,omitempty"`
	DraftID         int64  `json:"draft_id"`
	Text            string `json:"text"`
	ParseMode       string `json:"parse_mode,omitempty"`
}

// ThreadParam returns the message_thread_id to send for threadID, and false
// when the parameter must be left out. Sends reject the General topic id "1",
// but sendChatAction needs it to show the typing bubble in that topic (Hermes).
func ThreadParam(threadID string, forChatAction bool) (int, bool) {
	if threadID == "" || (threadID == generalTopicID && !forChatAction) {
		return 0, false
	}
	v, err := strconv.Atoi(threadID)
	if err != nil {
		return 0, false
	}
	return v, true
}

// envelope is the common reply shape of the Bot API.
type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Call sends params as a JSON POST to the method and decodes the result into
// out. It waits and retries on a rate-limit reply, at most 3 times.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	return c.call(ctx, method, params, out, callTimeout)
}

func (c *Client) call(ctx context.Context, method string, params any, out any, timeout time.Duration) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: encode parameters: %w", method, err)
	}
	return c.do(ctx, method, out, timeout, func() (io.Reader, string, func(), error) {
		return bytes.NewReader(body), "application/json", func() {}, nil
	})
}

// Upload sends one file and the text fields as a multipart POST to the method.
// The file goes in the form field fileField.
func (c *Client) Upload(ctx context.Context, method string, fields map[string]string, fileField, path string, out any) error {
	return c.do(ctx, method, out, uploadTimeout, func() (io.Reader, string, func(), error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, "", nil, err
		}
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			err := writeMultipart(mw, fields, fileField, filepath.Base(path), f)
			_ = f.Close()
			pw.CloseWithError(err)
		}()
		// The cleanup stops the writer goroutine when the request ends early.
		return pr, mw.FormDataContentType(), func() { _ = pr.Close() }, nil
	})
}

func writeMultipart(mw *multipart.Writer, fields map[string]string, fileField, name string, f io.Reader) error {
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile(fileField, name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	return mw.Close()
}

// do runs one request with the rate-limit retry loop. newBody makes a fresh
// body for each attempt.
func (c *Client) do(ctx context.Context, method string, out any, timeout time.Duration,
	newBody func() (io.Reader, string, func(), error)) error {
	for attempt := 0; ; attempt++ {
		env, err := c.once(ctx, method, timeout, newBody)
		if err != nil {
			c.logger().Warn("telegram request failed", "method", method, "err", err.Error())
			return err
		}
		if env.OK {
			if out == nil || len(env.Result) == 0 {
				return nil
			}
			if err := json.Unmarshal(env.Result, out); err != nil {
				return fmt.Errorf("telegram %s: decode result: %w", method, err)
			}
			return nil
		}
		apiErr := &APIError{Code: env.ErrorCode, Description: env.Description}
		if env.Parameters != nil {
			apiErr.RetryAfter = env.Parameters.RetryAfter
		}
		if apiErr.Code != http.StatusTooManyRequests || attempt >= maxRetries429 {
			return fmt.Errorf("telegram %s: %w", method, apiErr)
		}
		wait := time.Duration(max(apiErr.RetryAfter, 1)) * time.Second
		c.logger().Info("telegram rate limit, wait and retry", "method", method, "retry_after_s", apiErr.RetryAfter, "attempt", attempt+1)
		if err := c.wait(ctx, wait); err != nil {
			return fmt.Errorf("telegram %s: %w", method, err)
		}
	}
}

// once sends one request and decodes the reply envelope. A reply that is not
// JSON gives an error with the HTTP status.
func (c *Client) once(ctx context.Context, method string, timeout time.Duration,
	newBody func() (io.Reader, string, func(), error)) (envelope, error) {
	var env envelope
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	body, ctype, cleanup, err := newBody()
	if err != nil {
		return env, fmt.Errorf("telegram %s: %w", method, err)
	}
	defer cleanup()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), body)
	if err != nil {
		return env, fmt.Errorf("telegram %s: make request: %w", method, c.clean(err))
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := c.hc().Do(req)
	if err != nil {
		return env, fmt.Errorf("telegram %s: %w", method, c.clean(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return env, fmt.Errorf("telegram %s: read reply: %w", method, c.clean(err))
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode == http.StatusTooManyRequests {
			// A 429 with a body that is not JSON still gets the retry.
			return envelope{ErrorCode: http.StatusTooManyRequests, Description: "Too Many Requests"}, nil
		}
		return env, fmt.Errorf("telegram %s: HTTP %d: reply is not JSON", method, resp.StatusCode)
	}
	if !env.OK && env.ErrorCode == 0 {
		env.ErrorCode = resp.StatusCode
	}
	return env, nil
}

// GetMe returns the bot user.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.Call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for updates from offset. The request deadline is
// timeoutS + 10 seconds.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutS int) ([]Update, error) {
	var ups []Update
	params := map[string]any{
		"offset":          offset,
		"timeout":         timeoutS,
		"allowed_updates": []string{"message", "edited_message", "callback_query"},
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS+10)*time.Second)
	defer cancel()
	err := c.call(ctx, "getUpdates", params, &ups, 0)
	return ups, err
}

// SendMessage sends a text message.
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (Message, error) {
	var m Message
	err := c.Call(ctx, "sendMessage", p, &m)
	return m, err
}

// SendRichMessage sends a rich message, for example a Markdown table.
func (c *Client) SendRichMessage(ctx context.Context, p SendRichMessageParams) (Message, error) {
	var m Message
	err := c.Call(ctx, "sendRichMessage", p, &m)
	return m, err
}

// EditMessageText replaces the text of a message.
func (c *Client) EditMessageText(ctx context.Context, p EditMessageTextParams) (Message, error) {
	var m Message
	err := c.Call(ctx, "editMessageText", p, &m)
	return m, err
}

// DeleteMessage deletes a message.
func (c *Client) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	return c.Call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

// SendChatAction shows a chat action, for example "typing". A zero threadID
// is left out. Get threadID from ThreadParam(id, true).
func (c *Client) SendChatAction(ctx context.Context, chatID int64, threadID int, action string) error {
	params := map[string]any{"chat_id": chatID, "action": action}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}
	return c.Call(ctx, "sendChatAction", params, nil)
}

// SendMessageDraft streams a draft of a message in a private chat.
func (c *Client) SendMessageDraft(ctx context.Context, p SendMessageDraftParams) error {
	return c.Call(ctx, "sendMessageDraft", p, nil)
}

// AnswerCallbackQuery answers a button press. An empty text shows nothing.
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID, text string, showAlert bool) error {
	params := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		params["text"] = text
	}
	if showAlert {
		params["show_alert"] = true
	}
	return c.Call(ctx, "answerCallbackQuery", params, nil)
}

// SetMyCommands sets the bot command menu.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.Call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

// GetFile returns the download path of a file.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	var f File
	err := c.Call(ctx, "getFile", map[string]any{"file_id": fileID}, &f)
	return f, err
}

// DownloadFile saves the file at filePath (from GetFile) to dst. It writes a
// temporary file first and renames it, so dst is never partial.
func (c *Client) DownloadFile(ctx context.Context, filePath, dst string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, uploadTimeout)
		defer cancel()
	}
	u := strings.TrimRight(c.base(), "/") + "/file/bot" + c.Token + "/" + strings.TrimLeft(filePath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("telegram download: make request: %w", c.clean(err))
	}
	resp, err := c.hc().Do(req)
	if err != nil {
		err = c.clean(err)
		c.logger().Warn("telegram download failed", "err", err.Error())
		return fmt.Errorf("telegram download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram download: HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("telegram download: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return fmt.Errorf("telegram download: %w", err)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("telegram download: %w", c.clean(err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("telegram download: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("telegram download: %w", err)
	}
	return nil
}

func (c *Client) methodURL(method string) string {
	return strings.TrimRight(c.base(), "/") + "/bot" + c.Token + "/" + method
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return c.BaseURL
}

func (c *Client) hc() *http.Client {
	if c.HC == nil {
		return defaultHC
	}
	return c.HC
}

func (c *Client) logger() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// clean removes the request URL from err, because the URL holds the bot token.
// It keeps the error chain when the token is not in the remaining text.
func (c *Client) clean(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if c.Token != "" && strings.Contains(err.Error(), c.Token) {
		return errors.New(strings.ReplaceAll(err.Error(), c.Token, "<token>"))
	}
	return err
}
