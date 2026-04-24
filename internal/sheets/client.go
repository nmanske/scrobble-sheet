package sheets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"lastfm-sheet-sync/internal/googleauth"
	"lastfm-sheet-sync/internal/model"
)

const baseURL = "https://sheets.googleapis.com/v4/spreadsheets"

type Client struct {
	spreadsheetID string
	auth          *googleauth.Authenticator
	httpClient    *http.Client
}

type spreadsheetMetadata struct {
	Sheets []struct {
		Properties struct {
			SheetID int64  `json:"sheetId"`
			Title   string `json:"title"`
		} `json:"properties"`
	} `json:"sheets"`
}

type valuesGetResponse struct {
	Range  string          `json:"range"`
	Values [][]interface{} `json:"values"`
}

type batchUpdateRequest struct {
	ValueInputOption string                `json:"valueInputOption"`
	Data             []batchUpdateValueSet `json:"data"`
}

type batchUpdateValueSet struct {
	Range  string          `json:"range"`
	Values [][]interface{} `json:"values"`
}

type addSheetRequest struct {
	Requests []struct {
		AddSheet struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"addSheet"`
	} `json:"requests"`
}

func NewClient(spreadsheetID string, auth *googleauth.Authenticator, httpClient *http.Client) *Client {
	return &Client{spreadsheetID: spreadsheetID, auth: auth, httpClient: httpClient}
}

func (c *Client) EnsureSheet(ctx context.Context, sheetName string) error {
	meta, err := c.Metadata(ctx)
	if err != nil {
		return err
	}
	for _, sheet := range meta.Sheets {
		if sheet.Properties.Title == sheetName {
			return nil
		}
	}

	reqBody := addSheetRequest{}
	reqBody.Requests = append(reqBody.Requests, struct {
		AddSheet struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"addSheet"`
	}{})
	reqBody.Requests[0].AddSheet.Properties.Title = sheetName

	_, err = c.doJSON(ctx, http.MethodPost, "/:batchUpdate", reqBody, nil)
	return err
}

func (c *Client) Metadata(ctx context.Context) (*spreadsheetMetadata, error) {
	q := url.Values{}
	q.Set("fields", "sheets.properties(sheetId,title)")
	var out spreadsheetMetadata
	if _, err := c.doJSON(ctx, http.MethodGet, "?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) EnsureHeaderRow(ctx context.Context, sheetName string) error {
	rows, err := c.ReadRows(ctx, sheetName)
	if err != nil {
		return err
	}
	if len(rows) > 0 && len(rows[0]) >= 7 {
		first := rows[0]
		if strings.EqualFold(strings.TrimSpace(first[0]), model.DateHeader) && strings.EqualFold(strings.TrimSpace(first[1]), model.ArtistHeader) {
			return nil
		}
	}
	update := batchUpdateRequest{
		ValueInputOption: "USER_ENTERED",
		Data: []batchUpdateValueSet{{
			Range:  fmt.Sprintf("%s!A1:G1", quoteSheetName(sheetName)),
			Values: [][]interface{}{model.Headers()},
		}},
	}
	_, err = c.doJSON(ctx, http.MethodPost, "/values:batchUpdate", update, nil)
	return err
}

func (c *Client) ReadRows(ctx context.Context, sheetName string) ([][]string, error) {
	rng := fmt.Sprintf("/values/%s", url.PathEscape(fmt.Sprintf("%s!A:G", quoteSheetName(sheetName))))
	q := url.Values{}
	q.Set("majorDimension", "ROWS")

	var out valuesGetResponse
	if _, err := c.doJSON(ctx, http.MethodGet, rng+"?"+q.Encode(), nil, &out); err != nil {
		if strings.Contains(err.Error(), "Unable to parse range") || strings.Contains(err.Error(), "Requested entity was not found") {
			return [][]string{}, nil
		}
		return nil, err
	}

	rows := make([][]string, 0, len(out.Values))
	for _, row := range out.Values {
		current := make([]string, 0, len(row))
		for _, cell := range row {
			current = append(current, stringifyCell(cell))
		}
		rows = append(rows, current)
	}
	return rows, nil
}

func (c *Client) BatchWriteRows(ctx context.Context, sheetName string, rows []*model.SheetRow) error {
	if len(rows) == 0 {
		return nil
	}
	data := make([]batchUpdateValueSet, 0, len(rows))
	for _, row := range rows {
		data = append(data, batchUpdateValueSet{
			Range:  fmt.Sprintf("%s!A%d:G%d", quoteSheetName(sheetName), row.RowNumber, row.RowNumber),
			Values: [][]interface{}{row.ToValues()},
		})
	}
	body := batchUpdateRequest{ValueInputOption: "USER_ENTERED", Data: data}
	_, err := c.doJSON(ctx, http.MethodPost, "/values:batchUpdate", body, nil)
	return err
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body any, out any) ([]byte, error) {
	token, err := c.auth.AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	fullURL := baseURL + "/" + path.Clean(c.spreadsheetID)
	switch {
	case strings.HasPrefix(endpoint, "?"):
		fullURL += endpoint
	case strings.HasPrefix(endpoint, "/"):
		fullURL += endpoint
	default:
		fullURL += "/" + endpoint
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal sheets request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
	if err != nil {
		return nil, fmt.Errorf("build sheets request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request sheets API: %w", err)
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payload, fmt.Errorf("google sheets API error: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return payload, fmt.Errorf("decode sheets response: %w", err)
		}
	}
	return payload, nil
}

func stringifyCell(cell interface{}) string {
	switch v := cell.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	case bool:
		if v {
			return "TRUE"
		}
		return "FALSE"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func quoteSheetName(name string) string {
	escaped := strings.ReplaceAll(name, "'", "''")
	return "'" + escaped + "'"
}
