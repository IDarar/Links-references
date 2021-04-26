package server

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"

	"github.com/gin-gonic/gin"
)

var (
	findLinkRegex = regexp.MustCompile(`(?i)<a.*?href\s*?=\s*?"\s*?(.*?)\s*?".*?>`)
)
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type link struct {
	URL string `json:"url" binding:"required"`
}
type linkRefs struct {
	Link string      `json:"link"`
	Refs []*linkRefs `json:"refs"`
}

func Run() error {
	f, _ := os.OpenFile("logs.txt", os.O_WRONLY|os.O_CREATE, 0755)

	log.SetOutput(f)

	r := gin.Default()
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{"http://localhost:8080"}
	config.AllowCredentials = true

	r.Use(cors.New(config))
	//r.POST("/link", linkCrawler)
	r.GET("/ws", wsconn)
	s := &http.Server{
		Addr:         ":7777",
		Handler:      r,
		ReadTimeout:  time.Second * 60,
		WriteTimeout: time.Second * 60,
	}

	err := s.ListenAndServe()
	if err != nil {
		log.Fatal("Could not start server ... ", err)
		return err
	}
	return nil
}

/*func ws(conn *websocket.Conn, lChan <-chan string, close <-chan int, wg *sync.WaitGroup) error {
	defer wg.Done()

	for {
		select {
		case <-lChan:
			msg := <-lChan
			send := &wsMsg{URL: msg}
			err := conn.WriteJSON(send)
			if err != nil {
				log.Fatal("web socket error ", err)
				conn.Close()
				return err
			}
		case <-close:
			fmt.Print("closing ws")
			conn.Close()
			return nil
		}
	}
}*/

func ws(conn *websocket.Conn, lChan *chan string, close *chan int, wg *sync.WaitGroup) error {
	//defer wg.Done()

	for {
		select {
		case <-(*lChan):
			msg := <-(*lChan)
			fmt.Println("MSG TO WS", msg)
			err := conn.WriteMessage(1, []byte(msg))
			if err != nil {
				log.Fatal("web socket error ", err)
				conn.Close()
				return err
			}
		case <-(*close):
			fmt.Print("closing ws ")
			return nil
		}
	}
}

func wsconn(c *gin.Context) {
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Println(err)
		return
	}
	link, err := url.Parse(string(msg))
	if err != nil {
		newResponse(c, http.StatusBadRequest, "invalid input body")
		return
	}

	links := make(map[string]*url.URL)

	var mx = &sync.RWMutex{}
	var wg = &sync.WaitGroup{}
	str := linkRefs{Link: link.String()}

	lChan := make(chan string)
	close := make(chan int)

	//wg.Add(1)
	go ws(conn, &lChan, &close, wg)

	l, allLinks, err := InitCycleV1(link, &links, &str, mx, wg, &lChan, &close)

	if err != nil {
		newResponse(c, http.StatusBadRequest, err.Error())
		return
	}

	fmt.Println("struct is ", *l)
	uniqueLinks := make([]string, 0, len(*allLinks))

	for k := range *allLinks {
		uniqueLinks = append(uniqueLinks, k)
	}

	response := linksResponse{
		Link:               string(msg),
		UniqueRefs:         uniqueLinks,
		NumberOfUniqueRefs: len(*(allLinks)),
		References:         *l,
	}
	fmt.Println("uniqueLinks ", uniqueLinks)
	conn.WriteJSON(response)
	conn.Close()
	//closing ws

}
func InitCycleV1(link *url.URL,
	mLinks *map[string]*url.URL,
	str *linkRefs, mx *sync.RWMutex,
	wg *sync.WaitGroup,
	lChan *chan string,
	close *chan int) (*linkRefs, *map[string]*url.URL, error) {

	defer handleOutOfBounds()

	(*lChan) <- link.String()

	page, err := makeReqV2(link.String())
	if err != nil {
		fmt.Println("incorrect link, stop further reqs")
		return str, mLinks, err
	}

	(*mLinks)[link.String()] = link
	res := findLinkRegex.FindAllStringSubmatch(page, -1)
	wg.Add(len(res))
	for _, v := range res {
		urlLink := resolveURL(link, v[1])
		fmt.Println(len(res), " link ", v[1])
		time.Sleep(50 * time.Millisecond)

		go linkCycle(urlLink, mLinks, str, mx, wg, lChan)
	}

	wg.Wait()
	*close <- 1
	log.Info("map is ", mLinks)
	log.Info("map contains ", len(*mLinks), "links")
	fmt.Println("map is ", mLinks)
	fmt.Println("map contains ", len(*mLinks), "links")
	return str, mLinks, nil
}

type linksResponse struct {
	Link               string   `json:"link,omitempty"`
	UniqueRefs         []string `json:"unique_refs,omitempty"`
	NumberOfUniqueRefs int      `json:"number_of_unique_refs,omitempty"`
	References         linkRefs `json:"references,omitempty"`
}

func linkCycle(link *url.URL, mLinks *map[string]*url.URL, str *linkRefs, mx *sync.RWMutex, wg *sync.WaitGroup, lChan *chan string) {
	defer handleOutOfBounds()
	defer wg.Done()
	lowStr := &linkRefs{}
	lowStr.Link = link.String()
	(*lChan) <- link.String()

	mx.RLock()
	_, ok := (*mLinks)[link.String()]
	mx.RUnlock()

	if ok {
		log.Info("return from goroutine, link exists ", link.String())
		fmt.Println("return from goroutine, link exists ", link.String())
		return
	}

	mx.Lock()
	(*mLinks)[link.String()] = link
	str.Refs = append(str.Refs, lowStr)
	mx.Unlock()

	page, _ := makeReqV2(link.String())
	res := findLinkRegex.FindAllStringSubmatch(page, -1)
	fmt.Println(" goroutine lvl 2 ", "gets ", link, " link")
	log.Info(" goroutine lvl 2 ", "gets ", link, " link")

	wg.Add(len(res))
	for _, v := range res {
		urlLink := resolveURL(link, v[1])
		time.Sleep(50 * time.Millisecond)

		go linkCycle2(urlLink, mLinks, lowStr, mx, wg, lChan)
	}

}
func linkCycle2(link *url.URL, mLinks *map[string]*url.URL, str *linkRefs, mx *sync.RWMutex, wg *sync.WaitGroup, lChan *chan string) {
	defer handleOutOfBounds()
	defer wg.Done()
	lowStr := &linkRefs{}
	lowStr.Link = link.String()

	(*lChan) <- link.String()
	mx.RLock()
	_, ok := (*mLinks)[link.String()]
	mx.RUnlock()

	if ok {
		log.Info("return from goroutine lvl 3, LINK EXISTS ", link.String())
		fmt.Println("return from goroutine lvl 3, LINK EXISTS ", link.String())
		return
	}

	mx.Lock()
	(*mLinks)[link.String()] = link
	str.Refs = append(str.Refs, lowStr)
	mx.Unlock()

	fmt.Println(" goroutine lvl 3 ", "gets ", link, " link")
	log.Info(" goroutine lvl 3 ", "gets ", link, " link")

}

func makeReqV2(urlL string) (string, error) {
	defer handleOutOfBounds()

	resp, err := http.Get(urlL)

	fmt.Println("GET FROM ", urlL)
	log.Info("GET FROM ", urlL)

	if err != nil {
		fmt.Println("err", err)
		return "", err
	}
	body, _ := ioutil.ReadAll(resp.Body)
	//fmt.Println("response Body:", string(body))
	return string(body), nil
}

func resolveURL(relTo *url.URL, target string) *url.URL {
	tLen := len(target)
	if tLen == 0 {
		return nil
	}

	if tLen >= 1 && target[0] == '/' {
		if tLen >= 2 && target[1] == '/' {
			target = relTo.Scheme + ":" + target
		}
	}

	if targetURL, err := url.Parse(target); err == nil {
		return relTo.ResolveReference(targetURL)
	}

	return nil
}
func handleOutOfBounds() {
	if r := recover(); r != nil {
		fmt.Println("Recovering from panic: ", r)
	}
}
