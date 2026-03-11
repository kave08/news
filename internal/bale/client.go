package bale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const DefaultBaseURL = "https://tapi.bale.ai"

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type APIError struct {
	Method      string
	StatusCode  int
	ErrorCode   int
	Description string
}

func (e *APIError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("bale %s failed with status %d: %s", e.Method, e.StatusCode, e.Description)
	}
	return fmt.Sprintf("bale %s failed: %s", e.Method, e.Description)
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

type GetUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

type Update struct {
	UpdateID     int64    `json:"update_id"`
	Message      *Message `json:"message,omitempty"`
	ChannelPost  *Message `json:"channel_post,omitempty"`
}

type Message struct {
	MessageID  int64       `json:"message_id"`
	From       *User       `json:"from,omitempty"`
	SenderChat *Chat       `json:"sender_chat,omitempty"`
	Chat       Chat        `json:"chat"`
	Date       int64       `json:"date"`
	Text       string      `json:"text,omitempty"`
	Caption    string      `json:"caption,omitempty"`
	Photo      []PhotoSize `json:"photo,omitempty"`
	Video      *Video      `json:"video,omitempty"`
	Document   *Document   `json:"document,omitempty"`
	Audio      *Audio      `json:"audio,omitempty"`
	Voice      *Voice      `json:"voice,omitempty"`
	Sticker    *Sticker    `json:"sticker,omitempty"`
	Animation  *Animation  `json:"animation,omitempty"`
	VideoNote  *VideoNote  `json:"video_note,omitempty"`
	Contact    *Contact    `json:"contact,omitempty"`
	Location   *Location   `json:"location,omitempty"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type,omitempty"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type PhotoSize struct {
	FileID string `json:"file_id,omitempty"`
}

type Video struct {
	FileID string `json:"file_id,omitempty"`
}

type Document struct {
	FileID string `json:"file_id,omitempty"`
}

type Audio struct {
	FileID string `json:"file_id,omitempty"`
}

type Voice struct {
	FileID string `json:"file_id,omitempty"`
}

type Sticker struct {
	FileID string `json:"file_id,omitempty"`
}

type Animation struct {
	FileID string `json:"file_id,omitempty"`
}

type VideoNote struct {
	FileID string `json:"file_id,omitempty"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number,omitempty"`
}

type Location struct {
	Longitude float64 `json:"longitude,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
}

func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    baseURL,
		token:      strings.TrimSpace(token),
		httpClient: httpClient,
	}
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	if err := c.doJSON(ctx, "getMe", nil, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

func (c *Client) GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error) {
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.Timeout == 0 {
		req.Timeout = 20
	}

	var updates []Update
	if err := c.doJSON(ctx, "getUpdates", req, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *Client) doJSON(ctx context.Context, method string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal %s payload: %w", method, err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), body)
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute %s request: %w", method, err)
	}
	defer resp.Body.Close()

	limitedBody := io.LimitReader(resp.Body, 1<<20)
	var apiResp apiResponse[json.RawMessage]
	if err := json.NewDecoder(limitedBody).Decode(&apiResp); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !apiResp.OK {
		return &APIError{
			Method:      method,
			StatusCode:  resp.StatusCode,
			ErrorCode:   apiResp.ErrorCode,
			Description: apiResp.Description,
		}
	}

	if out == nil {
		return nil
	}
	if len(apiResp.Result) == 0 || errors.Is(json.Unmarshal(apiResp.Result, out), io.EOF) {
		return nil
	}
	if err := json.Unmarshal(apiResp.Result, out); err != nil {
		return fmt.Errorf("unmarshal %s result: %w", method, err)
	}

	return nil
}

func (c *Client) methodURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
}

func (u User) DisplayName() string {
	return displayName(u.FirstName, u.LastName, u.Username, u.ID)
}

func (c Chat) DisplayName() string {
	return displayName(c.Title, strings.TrimSpace(strings.Join([]string{c.FirstName, c.LastName}, " ")), c.Username, c.ID)
}

func displayName(primary, secondary, username string, id int64) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	if value := strings.TrimSpace(secondary); value != "" {
		return value
	}
	if value := strings.TrimSpace(username); value != "" {
		return "@" + value
	}
	if id != 0 {
		return fmt.Sprintf("%d", id)
	}
	return "unknown"
}
