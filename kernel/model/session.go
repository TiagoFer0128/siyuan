// SiYuan - Build Your Eternal Digital Garden
// Copyright (c) 2020-present, b3log.org
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package model

import (
	"net/http"
	"strings"

	"github.com/88250/gulu"
	ginSessions "github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/siyuan-note/siyuan/kernel/util"
	"github.com/steambap/captcha"
)

func LogoutAuth(c *gin.Context) {
	ret := gulu.Ret.NewResult()
	defer c.JSON(http.StatusOK, ret)

	if "" == Conf.AccessAuthCode {
		ret.Code = -1
		ret.Msg = Conf.Language(86)
		ret.Data = map[string]interface{}{"closeTimeout": 5000}
		return
	}

	session := ginSessions.Default(c)
	session.Options(ginSessions.Options{
		Path:   "/",
		MaxAge: -1,
	})
	session.Clear()
	if err := session.Save(); nil != err {
		util.LogErrorf("saves session failed: " + err.Error())
		ret.Code = -1
		ret.Msg = "save session failed"
	}
}

func LoginAuth(c *gin.Context) {
	ret := gulu.Ret.NewResult()
	defer c.JSON(http.StatusOK, ret)

	arg, ok := util.JsonArg(c, ret)
	if !ok {
		return
	}

	var inputCaptcha string
	session := util.GetSession(c)
	if session.NeedCaptcha() {
		_ = inputCaptcha
		//captchaArg := arg["captcha"]
		//if nil == captchaArg {
		//	c.Status(400)
		//	ret.Code = -1
		//	ret.Msg = "need input captcha"
		//	return
		//}
		//inputCaptcha = captchaArg.(string)
		//
		//if session.Captcha != inputCaptcha {
		//	ret.Code = -1
		//	ret.Msg = "invalid captcha"
		//	return
		//}
	}

	authCode := arg["authCode"].(string)
	if Conf.AccessAuthCode != authCode {
		ret.Code = -1
		ret.Msg = Conf.Language(83)

		session.WrongAuthCount++
		session.Captcha = gulu.Rand.String(7)
		if session.NeedCaptcha() {
			ret.Code = 1 // 需要渲染验证码
		}

		if err := session.Save(c); nil != err {
			util.LogErrorf("save session failed: " + err.Error())
			c.Status(500)
			return
		}
		return
	}

	session.AccessAuthCode = authCode
	session.WrongAuthCount = 0
	session.Captcha = gulu.Rand.String(7)
	if err := session.Save(c); nil != err {
		util.LogErrorf("save session failed: " + err.Error())
		c.Status(500)
		return
	}
}

func GetCaptcha(c *gin.Context) {
	img, err := captcha.NewMathExpr(150, 30)
	if nil != err {
		util.LogErrorf("generates captcha failed: " + err.Error())
		c.Status(500)
		return
	}

	session := util.GetSession(c)
	session.Captcha = img.Text
	if err = session.Save(c); nil != err {
		util.LogErrorf("save session failed: " + err.Error())
		c.Status(500)
		return
	}

	if err = img.WriteImage(c.Writer); nil != err {
		util.LogErrorf("writes captcha image failed: " + err.Error())
		c.Status(500)
		return
	}
	c.Status(200)
}

func CheckReadonly(c *gin.Context) {
	if util.ReadOnly {
		result := util.NewResult()
		result.Code = -1
		result.Msg = Conf.Language(34)
		result.Data = map[string]interface{}{"closeTimeout": 5000}
		c.JSON(200, result)
		c.Abort()
		return
	}
}

func CheckAuth(c *gin.Context) {
	//util.LogInfof("check auth for [%s]", c.Request.RequestURI)

	// 放过 /appearance/
	if strings.HasPrefix(c.Request.RequestURI, "/appearance/") ||
		strings.HasPrefix(c.Request.RequestURI, "/stage/build/export/") ||
		strings.HasPrefix(c.Request.RequestURI, "/stage/build/fonts/") ||
		strings.HasPrefix(c.Request.RequestURI, "/stage/protyle/") {
		c.Next()
		return
	}

	// 放过来自本机的某些请求
	if strings.HasPrefix(c.Request.RemoteAddr, "127.0.0.1") {
		if strings.HasPrefix(c.Request.RequestURI, "/assets/") || strings.HasPrefix(c.Request.RequestURI, "/history/assets/") {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.RequestURI, "/api/system/exit") {
			c.Next()
			return
		}
	}

	// 通过 Cookie
	session := util.GetSession(c)
	if session.AccessAuthCode == Conf.AccessAuthCode {
		c.Next()
		return
	}

	// 通过 API token
	if authHeader := c.GetHeader("Authorization"); "" != authHeader {
		if strings.HasPrefix(authHeader, "Token ") {
			token := strings.TrimPrefix(authHeader, "Token ")
			if Conf.Api.Token == token {
				c.Next()
				return
			}

			c.JSON(401, map[string]interface{}{"code": -1, "msg": "Auth failed"})
			c.Abort()
			return
		}
	}

	if strings.HasSuffix(c.Request.RequestURI, "/check-auth") {
		c.Next()
		return
	}

	if session.AccessAuthCode != Conf.AccessAuthCode {
		userAgentHeader := c.GetHeader("User-Agent")
		if strings.HasPrefix(userAgentHeader, "SiYuan/") || strings.HasPrefix(userAgentHeader, "Mozilla/") {
			if "GET" != c.Request.Method {
				c.JSON(401, map[string]interface{}{"code": -1, "msg": Conf.Language(156)})
				c.Abort()
				return
			}

			c.Redirect(302, "/check-auth")
			c.Abort()
			return
		}

		c.JSON(401, map[string]interface{}{"code": -1, "msg": "Auth failed"})
		c.Abort()
		return
	}

	c.Next()
}
