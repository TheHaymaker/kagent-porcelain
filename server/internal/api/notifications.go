package api

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wb "github.com/thehaymaker/kagent-porcelain/server/gen/workbench/v1"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

func kindName(k wb.NotificationKind) string { return enumSuffix(k.String(), "NOTIFICATION_KIND_") }

func kindValue(name string) wb.NotificationKind {
	return wb.NotificationKind(wb.NotificationKind_value["NOTIFICATION_KIND_"+strings.ToUpper(name)])
}

func channelName(c wb.Channel) string { return enumSuffix(c.String(), "CHANNEL_") }

func channelValue(name string) wb.Channel {
	return wb.Channel(wb.Channel_value["CHANNEL_"+strings.ToUpper(name)])
}

// ListNotifications implements NotificationService.
func (s *Server) ListNotifications(ctx context.Context, req *wb.ListNotificationsRequest) (*wb.ListNotificationsResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	pg := page(req.GetPage())
	list, unread, err := s.Store.ListNotifications(ctx, c.ID, req.GetUnreadOnly(), pg)
	if err != nil {
		return nil, toStatus(err)
	}
	out := &wb.ListNotificationsResponse{UnreadCount: int32(unread), Page: nextPage(pg, len(list))}
	for _, n := range list {
		out.Notifications = append(out.Notifications, &wb.Notification{Id: n.ID, Kind: kindValue(n.Kind), Title: n.Title, Body: n.Body,
			Link: n.Link, SessionId: n.SessionID, ApprovalId: n.ApprovalID, AppId: n.AppID, Read: n.Read, CreatedAt: tsp(n.CreatedAt)})
	}
	return out, nil
}

// MarkRead implements NotificationService.
func (s *Server) MarkRead(ctx context.Context, req *wb.MarkReadRequest) (*wb.MarkReadResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Store.MarkRead(ctx, c.ID, req.GetIds(), req.GetAll()); err != nil {
		return nil, toStatus(err)
	}
	return &wb.MarkReadResponse{}, nil
}

func (s *Server) preferences(ctx context.Context, personID string) ([]*wb.Preference, error) {
	stored, err := s.Store.Preferences(ctx, personID)
	if err != nil {
		return nil, err
	}
	var out []*wb.Preference
	for v := range wb.NotificationKind_name {
		k := wb.NotificationKind(v)
		if k == wb.NotificationKind_NOTIFICATION_KIND_UNSPECIFIED {
			continue
		}
		chs, ok := stored[kindName(k)]
		if !ok {
			chs = notify.Defaults[kindName(k)]
		}
		p := &wb.Preference{Kind: k}
		for _, ch := range chs {
			p.Channels = append(p.Channels, channelValue(ch))
		}
		out = append(out, p)
	}
	return out, nil
}

// GetPreferences implements NotificationService.
func (s *Server) GetPreferences(ctx context.Context, _ *wb.GetPreferencesRequest) (*wb.GetPreferencesResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	prefs, err := s.preferences(ctx, c.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.GetPreferencesResponse{Preferences: sortPrefs(prefs)}, nil
}

// SetPreferences implements NotificationService. In-app is always on: it is
// the record the other channels link back to.
func (s *Server) SetPreferences(ctx context.Context, req *wb.SetPreferencesRequest) (*wb.SetPreferencesResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range req.GetPreferences() {
		if p.GetKind() == wb.NotificationKind_NOTIFICATION_KIND_UNSPECIFIED {
			return nil, status.Error(codes.InvalidArgument, "preference needs a kind")
		}
		chs := []string{notify.InApp}
		for _, ch := range p.GetChannels() {
			if ch != wb.Channel_CHANNEL_UNSPECIFIED && ch != wb.Channel_CHANNEL_IN_APP {
				chs = append(chs, channelName(ch))
			}
		}
		if err := s.Store.SetPreference(ctx, c.ID, kindName(p.GetKind()), chs); err != nil {
			return nil, toStatus(err)
		}
	}
	prefs, err := s.preferences(ctx, c.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	return &wb.SetPreferencesResponse{Preferences: sortPrefs(prefs)}, nil
}

func sortPrefs(p []*wb.Preference) []*wb.Preference {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && p[j].Kind < p[j-1].Kind; j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
	return p
}

// RegisterDevice implements NotificationService.
func (s *Server) RegisterDevice(ctx context.Context, req *wb.RegisterDeviceRequest) (*wb.RegisterDeviceResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	platform := map[wb.Platform]string{wb.Platform_PLATFORM_IOS: "ios", wb.Platform_PLATFORM_ANDROID: "android"}[req.GetPlatform()]
	if platform == "" || len(req.GetPushToken()) < 8 || len(req.GetPushToken()) > 4096 {
		return nil, status.Error(codes.InvalidArgument, "a platform and a push token are required")
	}
	if err := s.Store.RegisterDevice(ctx, store.Device{PushToken: req.GetPushToken(), PersonID: c.ID, Platform: platform, DeviceName: req.GetDeviceName()}); err != nil {
		return nil, toStatus(err)
	}
	return &wb.RegisterDeviceResponse{}, nil
}
