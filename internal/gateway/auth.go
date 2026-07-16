package gateway

import (
	"net/http"
	"strings"
)

const uiAuthCookieName = "llmswap_ui_token"

func bearerAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimSpace(r.Header.Get("Authorization"))
		if got != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func uiAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if uiTokenAuthorized(r, token) {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/ui" && r.URL.Query().Get("token") == token {
			http.SetCookie(w, &http.Cookie{
				Name:     uiAuthCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
			})
			http.Redirect(w, r, "/ui", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	})
}

func uiTokenAuthorized(r *http.Request, token string) bool {
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	if got == "Bearer "+token {
		return true
	}
	cookie, err := r.Cookie(uiAuthCookieName)
	if err != nil {
		return false
	}
	return cookie.Value == token
}
