package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go"
	"firebase.google.com/go/messaging"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Ambient struct {
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	HeatIndex   float64 `json:"heatIndex"`
	Movement    int     `json:"move"`
}

type LogTemperature struct {
	AdjTemperature float64 `json:"adj_temperature"`
	AvgTemperature float64 `json:"avg_temperature"`
}

var timeZone = time.FixedZone("CST", -6*3600)

func firebaseApp(ctx context.Context) (app *firebase.App, err error) {

	credentials := os.Getenv("FILENAME_CREDENTIALS")
	opts := []option.ClientOption{option.WithCredentialsFile(credentials)}

	app, err = firebase.NewApp(ctx, nil, opts...)

	if err != nil {
		return nil, err
	}

	return

}

func countDocs(ite *firestore.DocumentIterator) (count int) {

	for {

		_, err := ite.Next()

		if err == iterator.Done {
			return
		}

		if err != nil {
			return -1
		}

		count++

	}

}

func get12hrsWithSecs(t time.Time) string {

	time12 := t.Format(time.Kitchen)
	offset := len(time12) - 2
	return fmt.Sprintf("%s:%02d%s", time12[:offset], t.Second(), time12[offset:])

}

func sendPushNotification(ambient Ambient) (err error) {

	ctx := context.Background()
	app, err := firebaseApp(ctx)

	if err != nil {
		return
	}

	fcmClient, err := app.Messaging(ctx)

	if err != nil {
		return
	}

	dbClient, err := app.Firestore(ctx)

	if err != nil {
		return
	}

	data := map[string]string{
		"Title": "Alerta de Ambiente",
		"Body": fmt.Sprintf(
			"Temperatura: %.2f°C<br>Humedad: %.0f%%<br>Indice de Calor: %.2f°C",
			ambient.Temperature,
			ambient.Humidity,
			ambient.HeatIndex,
		),
		"Temp": "",
	}

	if ambient.Movement > 0 {

		data["Title"] = "¡Alguien ha entrado al site!"
		data["Body"] = "Se han detectado lecturas de movimiento."
		data["Move"] = ""
		delete(data, "Temp")

		t := time.Now().In(timeZone)
		hour := get12hrsWithSecs(t)
		collection := dbClient.Collection("movement")

		docs, err := collection.Snapshots(ctx).Query.Documents(ctx).GetAll()

		if err != nil {
			log.Println(err)
		}

		if len(docs) >= 7 {
			_, err = docs[0].Ref.Delete(ctx)

			if err != nil {
				log.Println(err)
			}
		}

		year, month, day := t.Date()

		doc := collection.Doc(fmt.Sprintf("%d-%d-%d", year, month, day))
		snapshot, err := doc.Get(ctx)

		if status.Code(err) == codes.NotFound {

			doc.Set(ctx, map[string]interface{}{
				"move_logs": []string{hour},
			})

		} else if err != nil {

			log.Println(err)

		} else {

			moves, _ := snapshot.DataAt("move_logs")
			logs := moves.([]interface{})
			logs = append(logs, hour)

			_, err = snapshot.Ref.Update(ctx, []firestore.Update{{Path: "move_logs", Value: logs}})

			if err != nil {
				log.Println(err)
			}

		}

	}

	deviceTokens := []string{}
	tokens := dbClient.Collection("tokens").Documents(ctx)

	for {

		token, err := tokens.Next()

		if err == iterator.Done {
			break
		}

		if err != nil {
			return err
		}

		deviceTokens = append(deviceTokens, token.Data()["token"].(string))

	}

	_, err = fcmClient.SendMulticast(ctx, &messaging.MulticastMessage{
		Data:    data,
		Tokens:  deviceTokens,
		Android: &messaging.AndroidConfig{Priority: "high"},
	})

	if err != nil {
		return
	}

	return nil

}

func writeTemperature(temp LogTemperature) (err error) {

	ctx := context.Background()
	app, err := firebaseApp(ctx)

	if err != nil {
		return
	}

	dbClient, err := app.Firestore(ctx)

	if err != nil {
		return
	}

	values := dbClient.Collection("temperatures").Doc("values")
	data, err := values.Get(ctx)

	if err != nil {
		return
	}

	temperatures := data.Data()["Temperatures"].([]interface{})
	size := len(temperatures)

	for i := 0; i < 24-size; i++ {
		temperatures = append(temperatures, 0)
	}

	hour := time.Now()
	i := hour.In(timeZone).Hour()

	temperatures[i] = map[string]interface{}{
		"avg_temperature": math.Floor(temp.AvgTemperature*100) * 0.01,
		"adj_temperature": math.Floor(temp.AdjTemperature*100) * 0.01,
	}

	_, err = data.Ref.Update(ctx, []firestore.Update{{Path: "Temperatures", Value: temperatures}})

	if err != nil {
		return
	}

	return nil
}

func sendAll(w http.ResponseWriter, r *http.Request) {

	if r.Method != "POST" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid Method"))
		return
	}

	decoder := json.NewDecoder(r.Body)
	ambient := &Ambient{}

	if err := decoder.Decode(ambient); err != nil {

		log.Println("Error:", err)
		w.WriteHeader(http.StatusBadRequest)
		return

	}

	if err := sendPushNotification(*ambient); err != nil {

		log.Println("Error:", err)
		w.WriteHeader(http.StatusBadRequest)
		return

	}

}

func setTemperatures(w http.ResponseWriter, r *http.Request) {

	if r.Method != "POST" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid Method"))
		return
	}

	decoder := json.NewDecoder(r.Body)
	data := LogTemperature{}

	err := decoder.Decode(&data)

	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("MIssing data"))
		return
	}

	if err = writeTemperature(data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Fail in writing temperature"))
		return
	}

}

func main() {

	port := os.Getenv("PORT")

	if port == "" {
		port = "8000"
	}

	http.HandleFunc("/sendAll", sendAll)
	http.HandleFunc("/writeTemp", setTemperatures)

	fmt.Printf("Running in %s...\n", port)

	log.Fatal(
		http.ListenAndServe(fmt.Sprintf(":%s", port), nil),
	)

}
