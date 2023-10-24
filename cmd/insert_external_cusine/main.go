package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Taehoya/go-utils/pq"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"golang.org/x/oauth2/google"
	"gopkg.in/Iwark/spreadsheet.v2"
)

type config struct {
	kakaoAPIKey  string
	googleAPIKey string
}

func makeConfig() *config {
	return &config{
		kakaoAPIKey:  os.Getenv("KAKAO_API_KEY"),
		googleAPIKey: os.Getenv("GOOGLE_API_KEY"),
	}
}

var (
	sheetId = "1PfboVci0tyuw-JdoL6v1hoCVL2eoim2nkeZDm5fjX3Y"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("failed to loading .env file")
	}

	conf := makeConfig()
	db, err := pq.InitDB()

	if err != nil {
		log.Fatalf("err: %v", err)
	}
	defer db.Close()

	credBytes, err := base64.StdEncoding.DecodeString(conf.googleAPIKey)
	if err != nil {
		log.Fatalf("failed to decode google service account key")
	}

	gConfig, err := google.JWTConfigFromJSON(credBytes, "https://www.googleapis.com/auth/spreadsheets")
	if err != nil {
		log.Fatalf("err: %v", err)
	}

	client := gConfig.Client(context.TODO())
	service := spreadsheet.NewServiceWithClient(client)
	spreadsheet, err := service.FetchSpreadsheet(sheetId)

	if err != nil {
		log.Fatalf("failed to fetch spreadsheet")
	}

	sheet, err := spreadsheet.SheetByIndex(0)
	if err != nil {
		log.Fatal("failed to init google sheet service")
	}

	index := -1
	for _, row := range sheet.Rows {
		keyword := fmt.Sprintf("%s %s %s", row[0].Value, row[1].Value, row[2].Value)
		// keyword := fmt.Sprintf("%s %s", row[3].Value, row[2].Value)
		index++
		cusines, err := searchCusineByKeyWord(conf, keyword)
		if err != nil {
			log.Printf("keyword: %v, something goes wrong with kakao search api\n", keyword)
			continue
		}

		if len(cusines) == 0 {
			log.Printf("keyword: %v, no result\n", keyword)
			continue
		}

		cusine := cusines[0]
		cusineFieldMap, ok := cusine.(map[string]interface{})
		if !ok {
			log.Printf("Error: Invalid document format")
			continue
		}

		id := cusineFieldMap["id"].(string)
		x := cusineFieldMap["x"].(string)
		y := cusineFieldMap["y"].(string)
		placeUrl := cusineFieldMap["place_url"].(string)
		placeName := cusineFieldMap["place_name"].(string)
		currentTime := time.Now()

		// TODO: Seprate Insert external restaurant information and restaurant review
		// Our Postgrsql does not support ON CONFLICT DO NOTHING and lastInsertedIt from result
		stmt := `
	    	INSERT INTO
				public.external_restaurant_informations
	    		(external_uuid, "location", reference_link, updated_at, name)
	    	VALUES
				($1, ST_GeomFromText($2), $3, $4, $5)
			RETURNING id;
		`
		var lastInsertedID int
		err = db.QueryRow(stmt, id, fmt.Sprintf("POINT(%s %s)", x, y), placeUrl, currentTime, placeName).Scan(&lastInsertedID)
		if err != nil {
			log.Printf("err: %v", err)
			continue
		}

		userId := 1
		category := strings.Split(row[4].Value, ",")[0]
		summary := ""
		opnion := row[7].Value
		keywords := strings.Split(row[5].Value, ",")
		keywordsJSON := `{"` + strings.Join(keywords, `","`) + `"}`
		price := row[6].Value
		stmt = `
		INSERT INTO
			public.restaurant_reviews
			(user_id, category, summary, opinion, keywords, price, created_at, updated_at, external_restaurant_information_id)
		VALUES
			($1, $2, $3, $4, $5, $6, now(), now(), $7);
		`

		_, err = db.Exec(stmt, userId, category, summary, opnion, keywordsJSON, price, lastInsertedID)
		if err != nil {
			log.Fatal(err)
		}
	}

}

func searchCusineByKeyWord(conf *config, keyword string) ([]interface{}, error) {
	encodedKeyWord := url.QueryEscape(keyword)
	url := fmt.Sprintf("https://dapi.kakao.com/v2/local/search/keyword.json?query=%s", encodedKeyWord)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request")
	}

	kakaoAPIKey := fmt.Sprintf("KakaoAK %s", conf.kakaoAPIKey)

	req.Header.Set("Authorization", kakaoAPIKey)
	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to making request")
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to reading response body")
	}

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to decoding JSON")
	}

	cusines := result["documents"].([]interface{})
	return cusines, nil
}
