package main

import (
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

func (q *NgWebAPI) getMe(c *gin.Context) {
	userID := c.GetInt("userID")
	user := make(map[string]interface{})
	err := q.db.QueryRowx(
		"SELECT id, email, username FROM users WHERE id = $1",
		userID).MapScan(user)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	q.apiSuccess(c, 200, res{"user": user})
}
