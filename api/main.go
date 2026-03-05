package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

type Config struct {
	Name   string `json:"name"`
	Config string `json:"config"`
}

type HandshakeRequest struct {
	DeviceID string `json:"device_id"`
}

type hsresp struct {
	Token string `json:"token"`
}

type S_con struct {
	Sid string `json:"id"`
}

type Is struct {
	I1 string `json:"i1"`
}
type CrcsReq struct {
	Name           string `json:"name"`
	ApplyISettings bool   `json:"apply_i_settings"`
	ISettings      Is     `json:"i_settings"`
}

type CrcsResp struct {
	ConfigStr string `json:"config"`
}

var rdb *redis.Client

func initRedis() {
	rdb = redis.NewClient(&redis.Options{
		Addr:     "redis:6379",
		DB:       0,
		Password: os.Getenv("REDIS_PASS"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}
}

func main() {

	initRedis()

	r := gin.Default()

	r.POST("/handshake", func(c *gin.Context) {
		var req HandshakeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}

		err := rdb.HSet(c, "a_id:"+req.DeviceID, "token", req.DeviceID, "is_premium", false, "c_at", time.Now().Format("2006-01-02")).Err()
		if err != nil {
			fmt.Printf("HSET error: %v", err)
			c.JSON(500, gin.H{"error": "redis error"})
			return
		}

		response := hsresp{
			Token: "a_id:" + req.DeviceID,
		}
		c.JSON(200, response)
	})

	r.GET("/servers", func(c *gin.Context) {
		gbg := os.Getenv("I_GBG")
		var cfs []Config

		token := c.GetHeader("Authorization")

		am_ips := strings.Split(os.Getenv("AMN_IPS"), ",")
		am_nms := strings.Split(os.Getenv("SNMS"), ",")

		for iter, am_ip := range am_ips {
			c_exist, _ := rdb.HExists(c, token, "am:"+am_ip).Result()

			if c_exist {
				cf_str, err := rdb.HGet(c, token, "am:"+am_ip).Result()
				if err != nil {
					fmt.Printf("Fail fetch cf str for am:%s", am_ip)
					continue
				}
				cfobj := Config{
					Name:   am_nms[iter],
					Config: cf_str,
				}
				cfs = append(cfs, cfobj)

				continue
			}

			req_url := "http://" + am_ip + ":8080"
			sreq, err := http.Get(req_url + "/api/servers")
			if err != nil {
				panic(fmt.Errorf("sreq send error: %v", err))
			}
			defer sreq.Body.Close()

			var amsrs []S_con
			sid := ""
			body, err := io.ReadAll(sreq.Body)
			if err != nil {
				panic(fmt.Errorf("sreq read error: %v", err))
			}
			if err := json.Unmarshal(body, &amsrs); err != nil {
				panic(fmt.Errorf("sreq parse error: %v", err))
			}
			if len(amsrs) > 0 {
				sid = amsrs[0].Sid
			}
			if sid == "" {
				panic("Empty sid")
			}

			payload := CrcsReq{
				Name:           token,
				ApplyISettings: true,
				ISettings: Is{
					I1: gbg,
				},
			}
			jsonData, err := json.Marshal(payload)
			if err != nil {
				fmt.Printf("Obj parse fail: %v\n", err)
				return
			}
			crcsurl := req_url + "/api/servers" + sid + "/client"
			client := &http.Client{Timeout: 10 * time.Second}
			crcsresp, err := client.Post(crcsurl, "application/json", bytes.NewBuffer(jsonData))
			if err != nil {
				fmt.Printf("Client create req fail: %v\n", err)
				return
			}
			defer crcsresp.Body.Close()

			var cfrep CrcsResp
			cfres, err := io.ReadAll(crcsresp.Body)
			if err != nil {
				panic(fmt.Errorf("conf resp read error: %v", err))
			}
			if err := json.Unmarshal([]byte(cfres), &cfrep); err != nil {
				fmt.Println("conf resp parse fail:", err)
				return
			}
			target := "[Interface]"
			startIndex := strings.Index(cfrep.ConfigStr, target)
			if startIndex == -1 {
				fmt.Println("Got garbage not config")
				continue
			}
			cf_str := cfrep.ConfigStr[startIndex:]
			err = rdb.HSet(c, token, "am:"+am_ip, cf_str).Err()
			if err != nil {
				cfobj := Config{
					Name:   am_nms[iter],
					Config: cf_str,
				}
				cfs = append(cfs, cfobj)
			}
		}

		response := gin.H{
			"configs": cfs,
		}
		c.JSON(200, response)
	})

	r.Run(":9090")
}

func generateToken(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
