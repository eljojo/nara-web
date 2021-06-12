package nara

import (
	"encoding/json"
	"fmt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pbnjay/clustering"
	"github.com/sirupsen/logrus"
	"math/rand"
	"sort"
	"strings"
	"time"
)

type Network struct {
	Neighbourhood  map[string]*Nara
	LastHeyThere   int64
	skippingEvents bool
	local          *LocalNara
	Mqtt           mqtt.Client
}

func NewNetwork(localNara *LocalNara, host string, user string, pass string) *Network {
	network := &Network{local: localNara}
	network.Neighbourhood = make(map[string]*Nara)
	network.skippingEvents = false
	network.Mqtt = initializeMQTT(network.mqttOnConnectHandler(), network.meName(), host, user, pass)
	return network
}

func (network *Network) Start() {
	if token := network.Mqtt.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	go network.formOpinion()
	go network.observationMaintenance()
	go network.announceForever()
}

func (network *Network) meName() string {
	return network.local.Me.Name
}

func (network *Network) announce() {
	topic := fmt.Sprintf("%s/%s", "nara/newspaper", network.meName())

	// update neighbour's opinion on us
	network.recordObservationOnlineNara(network.meName())

	network.postEvent(topic, network.local.Me.Status)
}

func (network *Network) announceForever() {
	for {
		ts := network.local.Me.chattinessRate(20, 30)
		time.Sleep(time.Duration(ts) * time.Second)

		network.announce()
	}
}

func (network *Network) newspaperHandler(client mqtt.Client, msg mqtt.Message) {
	if network.local.Me.Status.Chattiness <= 10 && network.skippingEvents == false {
		logrus.Println("[warning] low chattiness, newspaper events may be dropped")
		network.skippingEvents = true
	} else if network.local.Me.Status.Chattiness > 10 && network.skippingEvents == true {
		logrus.Println("[recovered] chattiness is healthy again, not dropping events anymore")
		network.skippingEvents = false
	}
	if network.skippingEvents == true && rand.Intn(2) == 0 {
		return
	}
	if !strings.Contains(msg.Topic(), "nara/newspaper/") {
		return
	}
	var from = strings.Split(msg.Topic(), "nara/newspaper/")[1]

	if from == network.meName() {
		return
	}

	var status NaraStatus
	json.Unmarshal(msg.Payload(), &status)

	// logrus.Printf("newspaperHandler update from %s: %+v", from, status)

	other, present := network.Neighbourhood[from]
	if present {
		other.Status = status
		network.Neighbourhood[from] = other
	} else {
		logrus.Printf("%s posted a newspaper story (whodis?)", from)
		if network.local.Me.Status.Chattiness > 0 {
			network.heyThere()
		}
	}

	network.recordObservationOnlineNara(from)
	// inbox <- [2]string{msg.Topic(), string(msg.Payload())}
}

func (network *Network) heyThereHandler(client mqtt.Client, msg mqtt.Message) {
	nara := NewNara("")
	json.Unmarshal(msg.Payload(), nara)

	if nara.Name == network.meName() || nara.Name == "" {
		return
	}

	network.Neighbourhood[nara.Name] = nara
	logrus.Printf("%s says: hey there!", nara.Name)
	network.recordObservationOnlineNara(nara.Name)

	network.heyThere()
}

func (network *Network) recordObservationOnlineNara(name string) {
	observation := network.local.getObservation(name)

	if observation.StartTime == 0 || name == network.meName() {
		if name != network.meName() {
			logrus.Printf("observation: seen %s for the first time", name)
		}

		observation.Restarts = network.findRestartCountFromNeighbourhoodForNara(name)
		observation.StartTime = network.findStartingTimeFromNeighbourhoodForNara(name)
		observation.LastRestart = network.findLastRestartFromNeighbourhoodForNara(name)

		if observation.StartTime == 0 && name == network.meName() {
			observation.StartTime = time.Now().Unix()
		}

		if observation.LastRestart == 0 && name == network.meName() {
			observation.LastRestart = time.Now().Unix()
		}
	}

	if observation.Online != "ONLINE" && observation.Online != "" {
		observation.Restarts += 1
		observation.LastRestart = time.Now().Unix()
		logrus.Printf("observation: %s came back online", name)
	}

	observation.Online = "ONLINE"
	observation.LastSeen = time.Now().Unix()
	network.local.setObservation(name, observation)
}

func (network *Network) heyThere() {
	topic := "nara/plaza/hey_there"

	ts := network.local.Me.chattinessRate(10, 20)
	if (time.Now().Unix() - network.LastHeyThere) <= ts {
		return
	}

	network.LastHeyThere = time.Now().Unix()
	network.postEvent(topic, network.local.Me)
}

func (network *Network) chauHandler(client mqtt.Client, msg mqtt.Message) {
	nara := NewNara("")
	json.Unmarshal(msg.Payload(), nara)

	if nara.Name == network.meName() || nara.Name == "" {
		return
	}

	observation := network.local.getObservation(nara.Name)
	observation.Online = "OFFLINE"
	observation.LastSeen = time.Now().Unix()
	network.local.setObservation(nara.Name, observation)
	network.Neighbourhood[nara.Name] = nara

	network.local.Me.forgetPing(nara.Name)

	logrus.Printf("%s: chau!", nara.Name)
}

func (network *Network) Chau() {
	topic := "nara/plaza/chau"
	logrus.Printf("posting to %s", topic)

	observation := network.local.getMeObservation()
	observation.Online = "OFFLINE"
	observation.LastSeen = time.Now().Unix()
	network.local.setMeObservation(observation)

	network.postEvent(topic, network.local.Me)
}

func (network *Network) formOpinion() {
	time.Sleep(40 * time.Second)
	logrus.Printf("🕵️  forming opinions...")
	for name, _ := range network.Neighbourhood {
		observation := network.local.getObservation(name)
		startTime := network.findStartingTimeFromNeighbourhoodForNara(name)
		if startTime > 0 {
			observation.StartTime = startTime
		} else {
			logrus.Printf("couldn't adjust startTime for %s based on neighbour disagreement", name)
		}
		restarts := network.findRestartCountFromNeighbourhoodForNara(name)
		if restarts > 0 {
			observation.Restarts = restarts
		} else {
			logrus.Printf("couldn't adjust restart count for %s based on neighbour disagreement", name)
		}
		lastRestart := network.findLastRestartFromNeighbourhoodForNara(name)
		if lastRestart > 0 {
			observation.LastRestart = lastRestart
		} else {
			logrus.Printf("couldn't adjust last restart date for %s based on neighbour disagreement", name)
		}
		network.local.setObservation(name, observation)
	}
}

func (network *Network) findStartingTimeFromNeighbourhoodForNara(name string) int64 {
	times := make(map[int64]int)

	for _, nara := range network.Neighbourhood {
		observed_start_time := nara.getObservation(name).StartTime
		if observed_start_time > 0 {
			times[observed_start_time] += 1
		}
	}

	var startTime int64
	maxSeen := 0
	one_third := len(times) / 3

	for time, count := range times {
		if count > maxSeen && count > one_third {
			maxSeen = count
			startTime = time
		}
	}

	return startTime
}

func (network *Network) findRestartCountFromNeighbourhoodForNara(name string) int64 {
	values := make(map[int64]int)

	for _, nara := range network.Neighbourhood {
		restarts := nara.getObservation(name).Restarts
		values[restarts] += 1
	}

	var result int64
	maxSeen := 0

	for restarts, count := range values {
		if count > maxSeen && restarts > 0 {
			maxSeen = count
			result = restarts
		}
	}

	return result
}

func (network *Network) findLastRestartFromNeighbourhoodForNara(name string) int64 {
	values := make(map[int64]int)

	for _, nara := range network.Neighbourhood {
		last_restart := nara.getObservation(name).LastRestart
		if last_restart > 0 {
			values[last_restart] += 1
		}
	}

	var result int64
	maxSeen := 0
	one_third := len(values) / 3

	for last_restart, count := range values {
		if count > maxSeen && count > one_third {
			maxSeen = count
			result = last_restart
		}
	}

	return result
}

func (network *Network) observationMaintenance() {
	for {
		now := time.Now().Unix()

		for name, observation := range network.local.Me.Status.Observations {
			// only do maintenance on naras that are online
			if observation.Online != "ONLINE" {
				if observation.ClusterName != "" {
					// reset cluster for offline naras
					observation.ClusterName = ""
					network.local.setObservation(name, observation)
				}

				continue
			}

			// mark missing after 100 seconds of no updates
			if (now-observation.LastSeen) > 100 && !network.skippingEvents {
				observation.Online = "MISSING"
				network.local.setObservation(name, observation)
				logrus.Printf("observation: %s has disappeared", name)
			}
		}

		network.calculateClusters()

		time.Sleep(1 * time.Second)
	}
}

var clusterNames = []string{"olive", "peach", "sand", "ocean", "basil", "papaya", "brunch", "sorbet", "margarita", "bohemian", "terracotta"}

func (network *Network) calculateClusters() {
	distanceMap := network.prepareClusteringDistanceMap()
	clusters := clustering.NewDistanceMapClusterSet(distanceMap)

	// the Threshold defines how mini ms between nodes to consider as one cluster
	clustering.Cluster(clusters, clustering.Threshold(50), clustering.CompleteLinkage())
	sortedClusters := network.sortClusters(clusters)

	for clusterIndex, cluster := range sortedClusters {
		for _, name := range cluster {
			observation := network.local.getObservation(name)
			observation.ClusterName = clusterNames[clusterIndex]
			network.local.setObservation(name, observation)
		}
	}

	// set own neighbourhood
	observation := network.local.getMeObservation()
	network.local.Me.Status.Barrio = observation.ClusterName
}

func (network *Network) prepareClusteringDistanceMap() clustering.DistanceMap {
	distanceMap := make(clustering.DistanceMap)

	for _, nara := range network.Neighbourhood {
		// first create distance map with all pings from the perspective of each neighbour
		distanceMap[nara.Name] = nara.pingMap()
	}

	distanceMap[network.meName()] = network.local.Me.pingMap()

	return distanceMap
}

func (network *Network) sortClusters(clusters clustering.ClusterSet) [][]string {
	res := make([][]string, 0)

	clusters.EachCluster(-1, func(clusterIndex int) {
		cl := make([]string, 0)
		clusters.EachItem(clusterIndex, func(nameInterface clustering.ClusterItem) {
			name := nameInterface.(string)
			cl = append(cl, name)
		})
		res = append(res, cl)
	})

	sort.Slice(res, func(i, j int) bool {
		oldestI := network.oldestStarTimeForCluster(res[i])
		oldestJ := network.oldestStarTimeForCluster(res[j])

		// tie-break by oldest start time when clusters are same size otherwise sort by size
		if len(res[i]) == len(res[j]) {
			return oldestI < oldestJ
		} else {
			return len(res[i]) > len(res[j])
		}
	})

	return res
}

func (network *Network) oldestStarTimeForCluster(cluster []string) int64 {
	oldest := int64(0)
	for _, name := range cluster {
		obs := network.local.getObservation(name)
		if (obs.StartTime > 0 && obs.StartTime < oldest) || oldest == 0 {
			oldest = obs.StartTime
		}
	}
	return oldest
}

func (network *Network) pingHandler(client mqtt.Client, msg mqtt.Message) {
	var pingEvent PingEvent
	json.Unmarshal(msg.Payload(), &pingEvent)
	network.storePingEvent(pingEvent)
	logrus.Debugf("ping from %s to %s is %.2fms", pingEvent.From, pingEvent.To, pingEvent.TimeMs)
}

func (network *Network) postPing(ping PingEvent) {
	topic := fmt.Sprintf("%s/%s/%s", "nara/ping", ping.From, ping.To)
	network.postEvent(topic, ping)
}

func (network *Network) postEvent(topic string, event interface{}) {
	logrus.Debugf("posting on %s", topic)

	payload, err := json.Marshal(event)
	if err != nil {
		fmt.Println(err)
		return
	}
	token := network.Mqtt.Publish(topic, 0, false, string(payload))
	token.Wait()
}
