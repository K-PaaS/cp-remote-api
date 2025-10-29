package router

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var JwtSecret = os.Getenv("JWT_SECRET")

func CORSMiddleware() gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE"},
		AllowHeaders:     []string{"*"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: false,
	})
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {

		var tokenString string

		authHeader := c.GetHeader("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		} else {
			wsHeader := c.GetHeader("Sec-WebSocket-Protocol")
			parts := strings.Split(wsHeader, ",")
			if len(parts) == 2 && strings.TrimSpace(parts[0]) == "bearer" {
				tokenString = strings.TrimSpace(parts[1])
			}
		}

		if tokenString == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, "MISSING_JWT")
			return
		}

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if token.Method.Alg() != jwt.SigningMethodHS512.Alg() {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return []byte(JwtSecret), nil
		})
		//token, err := jwt.NewParser(jwt.WithExpirationRequired).Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		//	if token.Method.Alg() != jwt.SigningMethodHS512.Alg() {
		//		return nil, jwt.ErrTokenSignatureInvalid
		//	}
		//	return []byte(JwtSecret), nil
		//})

		if err != nil || !token.Valid {
			slog.Error("Invalid JWT", "err", err.Error())
			if errors.Is(err, jwt.ErrTokenExpired) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, "TOKEN_EXPIRED")
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, "TOKEN_FAILED")
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			slog.Error("Invalid claims")
			c.AbortWithStatusJSON(http.StatusUnauthorized, "TOKEN_FAILED")
			return
		}

		if expRaw, ok := claims["exp"].(float64); ok {
			exp := int64(expRaw)
			if exp < time.Now().Unix() {
				c.AbortWithStatusJSON(http.StatusUnauthorized, "TOKEN_EXPIRED")
				return
			}
		} else {
			slog.Error("Missing exp claim")
			c.AbortWithStatusJSON(http.StatusUnauthorized, "TOKEN_FAILED")
			return
		}

		userId, ok := claims["userAuthId"].(string)
		if !ok {
			slog.Error("Missing userId claim") // ApiAccessDenied
			c.AbortWithStatusJSON(http.StatusUnauthorized, "ApiAccessDenied")
			return
		}
		fmt.Print(userId)
		/*
			isAdmin, err := i.IsAdminCheck(userId)
			if err != nil {
				slog.Info("Failed to admin check api call", "err", err)
				response.ServerError(c)
				return
			}

			if !isAdmin {
				slog.Info("Not authorized to access this api")
				response.Unauthorized(c, errmsg.ApiAccessDenied)
				return
			}*/

		c.Set("claims", claims)
		c.Next()
	}
}
