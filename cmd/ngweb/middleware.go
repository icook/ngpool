package main

import (
	"fmt"
	"github.com/dgrijalva/jwt-go"
	"github.com/gin-gonic/gin"
	"strings"
)

type customClaims struct {
	UserID int `json:"user_id"`
	jwt.StandardClaims
}

func (q *NgWebAPI) authMiddleware(c *gin.Context) {
	authHeader := c.Request.Header.Get("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		c.Abort()
		q.apiError(c, 403, APIError{
			Code:  "invalid_auth",
			Title: "Malformed authorization header (did you miss 'HMAC-SHA256' or 'Bearer' prefix?)"})
		return
	}
	if parts[0] == "Bearer" {
		token, err := jwt.ParseWithClaims(parts[1], &customClaims{},
			func(token *jwt.Token) (interface{}, error) {
				if signMethod != token.Method {
					return nil, fmt.Errorf("invalid algo")
				}
				return []byte(q.config.GetString("JWTSecret")), nil
			})
		if err != nil {
			c.Abort()
			q.apiError(c, 403, APIError{
				Code:  "invalid_auth",
				Title: "Malformed token format"})
			return
		}
		claims := token.Claims.(*customClaims)
		c.Set("userID", claims.UserID)
	} else {
		c.Abort()
		q.apiError(c, 403, APIError{
			Code:  "invalid_auth",
			Title: "Malformed authorization header (invalid auth type)"})
		return
	}

	c.Next()
}
