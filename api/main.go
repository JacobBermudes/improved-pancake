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
	Client Clops `json:"client"`
}
type Clops struct {
	Clid string `json:"id"`
}

type Getcfg struct {
	Clncfg string `json:"clean_config"`
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
		token = strings.TrimPrefix(token, "Bearer ")

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
			admin_panel := &http.Client{Timeout: 10 * time.Second}

			sr_req, _ := http.NewRequest("GET", req_url+"/api/servers", nil)
			sr_req.SetBasicAuth(os.Getenv("CONTROL_USNM"), os.Getenv("CONTROL_PASD"))
			sr_res, err := admin_panel.Do(sr_req)
			if err != nil {
				panic(fmt.Errorf("sreq send error: %v", err))
			}
			defer sr_res.Body.Close()

			var amsrs []S_con
			sid := ""
			sr_r_j, err := io.ReadAll(sr_res.Body)
			if err != nil {
				panic(fmt.Errorf("sreq read error: %v", err))
			}
			if err := json.Unmarshal(sr_r_j, &amsrs); err != nil {
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
			crcs_req, _ := http.NewRequest("POST", req_url+"/api/servers/"+sid+"/clients", bytes.NewBuffer(jsonData))
			crcs_req.Header.Set("Content-Type", "application/json")
			crcs_req.SetBasicAuth(os.Getenv("CONTROL_USNM"), os.Getenv("CONTROL_PASD"))
			crcsresp, err := admin_panel.Do(crcs_req)
			if err != nil {
				fmt.Printf("Client create req fail: %v\n", err)
				return
			}
			defer crcsresp.Body.Close()

			var cfrep CrcsResp
			cfres, err := io.ReadAll(crcsresp.Body)
			if err != nil {
				panic(fmt.Errorf("clid resp read error: %v", err))
			}
			if err := json.Unmarshal(cfres, &cfrep); err != nil {
				fmt.Println("clid resp parse fail:", err)
				return
			}
			rdb.HSet(c, token, "am:id:"+am_ip, cfrep.Client.Clid).Err()

			getcfgurl := req_url + "/api/servers/" + sid + "/clients/" + cfrep.Client.Clid + "/config-both"
			getcfg_req, _ := http.NewRequest("GET", getcfgurl, bytes.NewBuffer(jsonData))
			getcfg_req.SetBasicAuth(os.Getenv("CONTROL_USNM"), os.Getenv("CONTROL_PASD"))
			getcfg_resp, err := admin_panel.Do(getcfg_req)
			if err != nil {
				fmt.Printf("Client create req fail: %v\n", err)
				return
			}
			defer getcfg_resp.Body.Close()

			var concfg Getcfg
			getcfgbody, err := io.ReadAll(getcfg_resp.Body)
			if err != nil {
				panic(fmt.Errorf("conf resp read error: %v", err))
			}
			if err = json.Unmarshal(getcfgbody, &concfg); err != nil {
				fmt.Println("conf resp parse fail:", err)
				return
			}

			err = rdb.HSet(c, token, "am:"+am_ip, concfg.Clncfg).Err()
			if err != nil {
				cfobj := Config{
					Name:   am_nms[iter],
					Config: concfg.Clncfg,
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
