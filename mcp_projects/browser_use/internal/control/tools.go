package control

import (
	"context"
	"encoding/json"
	"time"

	"browseruse/internal/audit"
	"browseruse/internal/browser"
	"browseruse/internal/config"
	"browseruse/internal/script"
)

type outcome struct {
	Data  any
	TabID string
}

func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func boolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func (s *Server) tabID(id string) string {
	if id == "" {
		return s.mgr.ActiveID()
	}
	return id
}

func (s *Server) execute(ctx context.Context, client, name string, raw json.RawMessage) (*outcome, error) {
	switch name {
	case "status":
		var req struct {
			IncludeTabs bool `json:"include_tabs"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		data := map[string]any{
			"app":           config.AppName,
			"control":       s.mgr.State(),
			"active_tab_id": s.mgr.ActiveID(),
			"tabs_count":    len(s.mgr.Tabs()),
			"downloads_dir": s.mgr.DownloadsDir(),
		}
		if a := s.mgr.Active(); a != nil {
			info := a.Info(true)
			data["active_tab"] = info
		}
		if req.IncludeTabs {
			data["tabs"] = s.mgr.Tabs()
		}
		return &outcome{Data: data, TabID: s.mgr.ActiveID()}, nil

	case "tabs":
		var req struct {
			Action   string `json:"action"`
			URL      string `json:"url"`
			Tab      string `json:"tab"`
			Activate *bool  `json:"activate"`
			Force    bool   `json:"force"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		switch req.Action {
		case "list", "":
			return &outcome{Data: map[string]any{"tabs": s.mgr.Tabs()}, TabID: s.mgr.ActiveID()}, nil
		case "new":
			activate := boolDefault(req.Activate, true)
			t, err := s.mgr.NewTab(ctx, req.URL, browser.TabOptions{Owner: browser.OwnerAgent, Activate: activate})
			if err != nil {
				return nil, err
			}
			return &outcome{Data: map[string]any{"tab": t.Info(activate)}, TabID: t.ID}, nil
		case "close":
			id := s.tabID(req.Tab)
			if err := s.mgr.CloseTab(id, req.Force); err != nil {
				return nil, err
			}
			return &outcome{Data: map[string]any{"closed": id}, TabID: s.mgr.ActiveID()}, nil
		case "select":
			id := s.tabID(req.Tab)
			if err := s.mgr.Activate(id); err != nil {
				return nil, err
			}
			return &outcome{Data: map[string]any{"active": id}, TabID: id}, nil
		default:
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "unknown tabs action: " + req.Action}
		}

	case "navigate":
		var req struct {
			Action    string `json:"action"`
			URL       string `json:"url"`
			Tab       string `json:"tab"`
			WaitUntil string `json:"wait_until"`
			TimeoutMS int    `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.Action == "" {
			req.Action = "goto"
		}
		if req.Action == "goto" && req.URL == "" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "url is required for goto"}
		}
		info, err := s.mgr.Navigate(ctx, req.Tab, client, req.Action, req.URL, browser.NavigateOptions{
			WaitUntil: req.WaitUntil, Timeout: time.Duration(req.TimeoutMS) * time.Millisecond,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: map[string]any{"navigated": req.Action != "stop", "tab": info}, TabID: info.ID}, nil

	case "snapshot":
		var req struct {
			Tab      string `json:"tab"`
			Scope    string `json:"scope"`
			MaxChars int    `json:"max_chars"`
			Filter   string `json:"filter"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.MaxChars <= 0 {
			req.MaxChars = s.cfg.Agent.SnapshotMaxChars
		}
		id := s.tabID(req.Tab)
		snap, err := s.mgr.Snapshot(ctx, id, client, browser.SnapshotOptions{
			Scope: req.Scope, MaxChars: req.MaxChars, Filter: req.Filter,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: snap, TabID: id}, nil

	case "read":
		var req struct {
			Tab      string `json:"tab"`
			Selector string `json:"selector"`
			MaxChars int    `json:"max_chars"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		id := s.tabID(req.Tab)
		r, err := s.mgr.Read(ctx, id, client, req.Selector, req.MaxChars)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: r, TabID: id}, nil

	case "script":
		if !s.cfg.Script.Enabled {
			return nil, &browser.Error{Code: browser.CodeScriptUnsupported, Message: "scripts are disabled"}
		}
		var req struct {
			Source          string          `json:"source"`
			Language        string          `json:"language"`
			Args            json.RawMessage `json:"args"`
			TimeoutMS       int             `json:"timeout_ms"`
			MaxOutputBytes  int             `json:"max_output_bytes"`
			ReleaseOnFinish *bool           `json:"release_on_finish"`
			Tab             string          `json:"tab"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.Source == "" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "source is required"}
		}
		if req.Tab != "" {
			if err := s.mgr.Activate(req.Tab); err != nil {
				return nil, err
			}
		}
		if !s.scriptActive.CompareAndSwap(false, true) {
			return nil, &browser.Error{Code: browser.CodeScriptBusy, Message: "another script is running"}
		}
		defer s.scriptActive.Store(false)
		res, err := script.Run(ctx, &scriptExecutor{s: s}, s.cfg.Script, script.Request{
			Source: req.Source, Language: req.Language, Args: req.Args,
			Timeout:         time.Duration(req.TimeoutMS) * time.Millisecond,
			MaxOutput:       req.MaxOutputBytes,
			ReleaseOnFinish: boolDefault(req.ReleaseOnFinish, s.cfg.Script.ReleaseOnFinish),
		})
		if err != nil {
			if be, ok := err.(*browser.Error); ok && res != nil {
				be.Details = map[string]any{"script": res}
			}
			return nil, err
		}
		return &outcome{Data: res, TabID: s.mgr.ActiveID()}, nil

	case "network":
		var req struct {
			Action      string `json:"action"`
			Tab         string `json:"tab"`
			URLContains string `json:"url_contains"`
			Method      string `json:"method"`
			Status      int    `json:"status"`
			Since       string `json:"since"`
			Limit       int    `json:"limit"`
			IncludeBody bool   `json:"include_body"`
			MaxBody     int    `json:"max_body"`
			ID          string `json:"id"`
			TimeoutMS   int    `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		res, err := s.mgr.Network(ctx, req.Tab, client, browser.NetworkOptions{
			Action:      req.Action,
			URLContains: req.URLContains,
			Method:      req.Method,
			Status:      req.Status,
			Since:       req.Since,
			Limit:       req.Limit,
			IncludeBody: req.IncludeBody,
			MaxBody:     req.MaxBody,
			ID:          req.ID,
			WaitMS:      req.TimeoutMS,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: res, TabID: s.mgr.ActiveID()}, nil

	case "frames":
		var req struct {
			Action string `json:"action"`
			Tab    string `json:"tab"`
			Frame  string `json:"frame"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		res, err := s.mgr.Frames(ctx, req.Tab, client, req.Action, req.Frame)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: res, TabID: s.mgr.ActiveID()}, nil

	case "files":
		var req struct {
			Tab      string   `json:"tab"`
			Action   string   `json:"action"`
			Ref      string   `json:"ref"`
			Selector string   `json:"selector"`
			Text     string   `json:"text"`
			Paths    []string `json:"paths"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.Action != "" && req.Action != "upload" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "unknown files action: " + req.Action}
		}
		target := browser.Target{Ref: req.Ref, Selector: req.Selector, Text: req.Text}
		if err := target.Validate(); err != nil {
			return nil, err
		}
		id := s.tabID(req.Tab)
		res, err := s.mgr.Upload(ctx, id, client, target, req.Paths)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: res, TabID: id}, nil

	case "cookies":
		var req struct {
			Action   string               `json:"action"`
			URL      string               `json:"url"`
			Name     string               `json:"name"`
			Value    string               `json:"value"`
			Domain   string               `json:"domain"`
			Path     string               `json:"path"`
			Secure   bool                 `json:"secure"`
			HTTPOnly bool                 `json:"http_only"`
			SameSite string               `json:"same_site"`
			Expires  string               `json:"expires"`
			Cookies  []browser.CookieInfo `json:"cookies"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		res, err := s.mgr.Cookies(browser.CookieOptions{
			Action: req.Action, URL: req.URL, Name: req.Name, Value: req.Value,
			Domain: req.Domain, Path: req.Path, Secure: req.Secure, HTTPOnly: req.HTTPOnly,
			SameSite: req.SameSite, Expires: req.Expires, Cookies: req.Cookies,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: res, TabID: s.mgr.ActiveID()}, nil

	case "screenshot":
		var req struct {
			Tab      string `json:"tab"`
			Annotate *bool  `json:"annotate"`
			Inline   bool   `json:"inline"`
			MaxWidth int    `json:"max_width"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		id := s.tabID(req.Tab)
		res, err := s.mgr.Screenshot(ctx, id, client, browser.ScreenshotOptions{
			Annotate: boolDefault(req.Annotate, true), Inline: req.Inline, MaxWidth: req.MaxWidth,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: res, TabID: id}, nil

	case "scroll":
		var req struct {
			Tab       string  `json:"tab"`
			Direction string  `json:"direction"`
			Amount    float64 `json:"amount"`
			Ref       string  `json:"ref"`
			Selector  string  `json:"selector"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		var target *browser.Target
		if req.Ref != "" || req.Selector != "" {
			t := browser.Target{Ref: req.Ref, Selector: req.Selector}
			if err := t.Validate(); err != nil {
				return nil, err
			}
			target = &t
		}
		id := s.tabID(req.Tab)
		pos, err := s.mgr.Scroll(ctx, id, client, req.Direction, req.Amount, target)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: map[string]any{"scroll": pos}, TabID: id}, nil

	case "hover":
		var req struct {
			Tab      string `json:"tab"`
			Ref      string `json:"ref"`
			Selector string `json:"selector"`
			Text     string `json:"text"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		target := browser.Target{Ref: req.Ref, Selector: req.Selector, Text: req.Text}
		if err := target.Validate(); err != nil {
			return nil, err
		}
		id := s.tabID(req.Tab)
		receipt, err := s.mgr.Hover(ctx, id, client, target)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: receipt, TabID: id}, nil

	case "eval":
		var req struct {
			Tab        string `json:"tab"`
			Expression string `json:"expression"`
			TimeoutMS  int    `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.Expression == "" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "expression is required"}
		}
		id := s.tabID(req.Tab)
		res, err := s.mgr.Eval(ctx, id, client, req.Expression, time.Duration(req.TimeoutMS)*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: res, TabID: id}, nil

	case "downloads":
		dls := s.mgr.Downloads()
		if dls == nil {
			dls = []browser.Download{}
		}
		return &outcome{Data: map[string]any{"downloads": dls, "dir": s.mgr.DownloadsDir()}, TabID: s.mgr.ActiveID()}, nil

	case "dialogs":
		var req struct {
			Tab string `json:"tab"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		id := s.tabID(req.Tab)
		list, err := s.mgr.Dialogs(ctx, id, client)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: map[string]any{"dialogs": list}, TabID: id}, nil

	case "click":
		var req struct {
			Tab       string         `json:"tab"`
			Ref       string         `json:"ref"`
			Selector  string         `json:"selector"`
			Text      string         `json:"text"`
			Point     *browser.Point `json:"point"`
			Button    string         `json:"button"`
			Count     int            `json:"count"`
			Modifiers []string       `json:"modifiers"`
			Force     bool           `json:"force"`
			Reveal    *bool          `json:"reveal"`
			WaitAfter string         `json:"wait_after"`
			TimeoutMS int            `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		target := browser.Target{Ref: req.Ref, Selector: req.Selector, Text: req.Text, Point: req.Point}
		if err := target.Validate(); err != nil {
			return nil, err
		}
		if req.Button == "" {
			req.Button = "left"
		}
		if req.Count == 0 {
			req.Count = 1
		}
		id := s.tabID(req.Tab)
		receipt, err := s.mgr.Click(ctx, id, client, target, browser.ClickOptions{
			Button: req.Button, Count: req.Count, Modifiers: req.Modifiers,
			Force: req.Force, Reveal: req.Reveal, WaitAfter: req.WaitAfter,
			Timeout: time.Duration(req.TimeoutMS) * time.Millisecond,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: receipt, TabID: id}, nil

	case "type":
		var req struct {
			Tab       string `json:"tab"`
			Ref       string `json:"ref"`
			Selector  string `json:"selector"`
			Text      string `json:"text"`
			Clear     *bool  `json:"clear"`
			Submit    bool   `json:"submit"`
			Slowly    bool   `json:"slowly"`
			WaitAfter string `json:"wait_after"`
			TimeoutMS int    `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.Ref == "" && req.Selector == "" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "ref or selector is required"}
		}
		id := s.tabID(req.Tab)
		receipt, err := s.mgr.Type(ctx, id, client, browser.Target{Ref: req.Ref, Selector: req.Selector}, req.Text, browser.TypeOptions{
			Clear: boolDefault(req.Clear, true), Submit: req.Submit, Slowly: req.Slowly,
			WaitAfter: req.WaitAfter, Timeout: time.Duration(req.TimeoutMS) * time.Millisecond,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: receipt, TabID: id}, nil

	case "select":
		var req struct {
			Tab       string   `json:"tab"`
			Ref       string   `json:"ref"`
			Selector  string   `json:"selector"`
			Values    []string `json:"values"`
			TimeoutMS int      `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.Ref == "" && req.Selector == "" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "ref or selector is required"}
		}
		if len(req.Values) == 0 {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "values is required"}
		}
		id := s.tabID(req.Tab)
		receipt, err := s.mgr.Select(ctx, id, client, browser.Target{Ref: req.Ref, Selector: req.Selector}, req.Values, time.Duration(req.TimeoutMS)*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: receipt, TabID: id}, nil

	case "press":
		var req struct {
			Tab       string `json:"tab"`
			Key       string `json:"key"`
			Ref       string `json:"ref"`
			Selector  string `json:"selector"`
			TimeoutMS int    `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.Key == "" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "key is required"}
		}
		var target *browser.Target
		if req.Ref != "" || req.Selector != "" {
			t := browser.Target{Ref: req.Ref, Selector: req.Selector}
			if err := t.Validate(); err != nil {
				return nil, err
			}
			target = &t
		}
		id := s.tabID(req.Tab)
		receipt, err := s.mgr.Press(ctx, id, client, req.Key, target, time.Duration(req.TimeoutMS)*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return &outcome{Data: receipt, TabID: id}, nil

	case "wait":
		var req struct {
			Tab       string `json:"tab"`
			For       string `json:"for"`
			Value     string `json:"value"`
			State     string `json:"state"`
			TimeoutMS int    `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		if req.For == "" {
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "for is required"}
		}
		if req.State == "" {
			req.State = "visible"
		}
		id := s.tabID(req.Tab)
		res, err := s.mgr.Wait(ctx, id, client, browser.WaitOptions{
			For: req.For, Value: req.Value, State: req.State,
			Timeout: time.Duration(req.TimeoutMS) * time.Millisecond,
		})
		if err != nil {
			return nil, err
		}
		return &outcome{Data: res, TabID: id}, nil

	case "control":
		var req struct {
			Action string `json:"action"`
			Tab    string `json:"tab"`
			Client string `json:"client"`
		}
		if err := decodeArgs(raw, &req); err != nil {
			return nil, bad(err)
		}
		switch req.Action {
		case "release":
			// No client releases every lease (an explicit agent request);
			// a client releases only that script/client's leases.
			st := s.mgr.Release(req.Tab, req.Client)
			return &outcome{Data: map[string]any{"control": st}, TabID: s.mgr.ActiveID()}, nil
		case "state", "":
			st := s.mgr.State()
			return &outcome{Data: map[string]any{"control": st}, TabID: s.mgr.ActiveID()}, nil
		default:
			return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "unknown control action: " + req.Action}
		}
	default:
		return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "unknown tool: " + name, Hint: "see /v1/tools"}
	}
}

type scriptExecutor struct{ s *Server }

func (e *scriptExecutor) Call(ctx context.Context, client, name string, args json.RawMessage) (any, error) {
	if name == "script" {
		return nil, &browser.Error{Code: browser.CodeScriptUnsupported, Message: "scripts cannot call script"}
	}
	start := time.Now()
	out, err := e.s.execute(ctx, client, name, args)
	tabID := ""
	if out != nil {
		tabID = out.TabID
	}
	if tabID == "" {
		tabID = e.s.mgr.ActiveID()
	}
	entry := auditEntry(start, client, name, tabID, args, err, e.s.mgr)
	e.s.record(entry)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return out.Data, nil
}

func auditEntry(start time.Time, client, name, tabID string, args json.RawMessage, err error, mgr *browser.Manager) audit.Entry {
	e := audit.Entry{
		TS: start, Tool: name, Tab: tabID, Client: client, OK: err == nil,
		MS: time.Since(start).Milliseconds(), Args: audit.RedactArgs(name, args),
	}
	if t := mgr.Tab(tabID); t != nil {
		e.URL = t.URL()
	}
	if err != nil {
		e.Code = asError(err).Code
	}
	return e
}

func bad(err error) *browser.Error {
	return &browser.Error{Code: browser.CodeBadRequest, Message: err.Error()}
}
