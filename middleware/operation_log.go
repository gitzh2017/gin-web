package middleware

import (
	"bytes"
	v1 "gin-web/api/v1"
	"gin-web/models"
	"gin-web/pkg/cache_service"
	"gin-web/pkg/global"
	"gin-web/pkg/request"
	"gin-web/pkg/response"
	"gin-web/pkg/utils"
	"github.com/casbin/casbin/v2/util"
	"github.com/gin-gonic/gin"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// 操作日志
func OperationLog(c *gin.Context) {
	// 开始时间
	startTime := time.Now()
	// 读取body参数
	var body []byte
	body, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		global.Log.Error("读取请求体失败: ", err)
	} else {
		// gin参数只能读取一次, 这里将其回写, 否则c.Next中的接口无法读取
		c.Request.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	}
	// 避免服务器出现异常, 这里用defer保证一定可以执行
	defer func() {
		// GET/OPTIONS请求比较频繁无需写入日志
		if c.Request.Method == http.MethodGet ||
			c.Request.Method == http.MethodOptions {
			return
		}
		// 用户自定义请求无需写入日志
		for _, s := range global.Conf.System.OperationLogDisabledPathArr {
			if strings.Contains(c.Request.URL.Path, s) {
				return
			}
		}

		// 结束时间
		endTime := time.Now()

		if len(body) == 0 {
			body = []byte("{}")
		}
		contentType := c.Request.Header.Get("Content-Type")
		// 二进制文件类型需要特殊处理
		if strings.Contains(contentType, "multipart/form-data") {

			contentTypeArr := strings.Split(contentType, "; ")
			if len(contentTypeArr) == 2 {
				// 读取boundary
				boundary := strings.TrimPrefix(contentTypeArr[1], "boundary=")
				// 通过multipart读取body参数全部内容
				b := strings.NewReader(string(body))
				r := multipart.NewReader(b, boundary)
				f, _ := r.ReadForm(int64(global.Conf.Upload.SingleMaxSize) << 20)
				defer f.RemoveAll()
				// 获取全部参数值
				params := make(map[string]string, 0)
				for key, val := range f.Value {
					// 保留第一个值就行了
					if len(val) > 0 {
						params[key] = val[0]
					}
				}
				params["content-type"] = "multipart/form-data"
				params["file"] = "二进制数据被忽略"
				// 将其转为json
				body = []byte(utils.Struct2Json(params))
			}
		}
		log := models.SysOperationLog{
			Model: models.Model{
				// 记录最后时间
				CreatedAt: models.LocalTime{
					Time: endTime,
				},
			},
			// Ip地址
			Ip: c.ClientIP(),
			// 请求方式
			Method: c.Request.Method,
			// 请求路径(去除url前缀)
			Path: strings.TrimPrefix(c.Request.URL.Path, "/"+global.Conf.System.UrlPathPrefix),
			// 请求体
			Body: string(body),
			// 请求耗时
			Latency: endTime.Sub(startTime),
			// 浏览器标识
			UserAgent: c.Request.UserAgent(),
		}

		// 清理事务
		c.Set("tx", "")
		// 获取当前登录用户
		user := v1.GetCurrentUser(c)

		// 用户名
		if user.Id > 0 {
			log.Username = user.Username
			log.RoleName = user.Role.Name
		} else {
			log.Username = "未登录"
			log.RoleName = "未登录"
		}

		// 获取当前接口
		cache := cache_service.New(c)
		apis, err := cache.GetApis(&request.ApiListRequestStruct{
			Method: log.Method,
			PageInfo: response.PageInfo{
				NoPagination: true,
			},
		})
		match := false
		if err == nil {
			for _, api := range apis {
				// 通过casbin KeyMatch2来匹配url规则
				match = util.KeyMatch2(log.Path, api.Path)
				if match {
					log.ApiDesc = api.Desc
					break
				}
			}
		}
		if !match {
			log.ApiDesc = "无"
		}

		// 获取Ip所在地
		log.IpLocation = utils.GetIpRealLocation(log.Ip)

		// 响应状态码
		log.Status = c.Writer.Status()
		// 响应数据
		resp, exists := c.Get(global.Conf.System.OperationLogKey)
		var data string
		if exists {
			data = utils.Struct2Json(resp)
			// 是自定义的响应类型
			if item, ok := resp.(response.Resp); ok {
				log.Status = item.Code
			}
		} else {
			data = "无"
		}
		// gzip压缩
		log.Data = data
		// 异步, 写入数据库
		go global.Mysql.Create(&log)
	}()
	c.Next()
}
