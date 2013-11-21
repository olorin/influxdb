package coordinator

import (
	"datastore"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"parser"
	"protocol"
)

type ProtobufRequestHandler struct {
	db            datastore.Datastore
	coordinator   Coordinator
	clusterConfig *ClusterConfiguration
	writeOk       protocol.Response_Type
}

func NewProtobufRequestHandler(db datastore.Datastore, coordinator Coordinator, clusterConfig *ClusterConfiguration) *ProtobufRequestHandler {
	return &ProtobufRequestHandler{db: db, coordinator: coordinator, writeOk: protocol.Response_WRITE_OK, clusterConfig: clusterConfig}
}

func (self *ProtobufRequestHandler) HandleRequest(request *protocol.Request, conn net.Conn) error {
	if *request.Type == protocol.Request_PROXY_WRITE {
		response := &protocol.Response{RequestId: request.Id, Type: &self.writeOk}

		self.db.LogRequestAndAssignId(request)
		err := self.db.WriteSeriesData(*request.Database, request.Series)
		if err != nil {
			return err
		}
		err = self.WriteResponse(conn, response)
		// TODO: add quorum writes?
		self.coordinator.ReplicateWrite(request)
		return err
	} else if *request.Type == protocol.Request_PROXY_DELETE {

	} else if *request.Type == protocol.Request_REPLICATION_WRITE {
		// TODO: check the request id and server and make sure it's next (+1 from last one from the server).
		//       If so, write. If not, request replay.
		// TODO: log replication writes so the can be retrieved from other servers
		self.db.WriteSeriesData(*request.Database, request.Series)
		return nil
	} else if *request.Type == protocol.Request_REPLICATION_DELETE {

	} else if *request.Type == protocol.Request_QUERY {
		go self.handleQuery(request, conn)
	} else {
		log.Println("unknown request type: ", request)
		return errors.New("Unknown request type")
	}
	return nil
}

func (self *ProtobufRequestHandler) handleQuery(request *protocol.Request, conn net.Conn) {
	var nextPoint *protocol.Point
	assignNextPointTimesAndSend := func(series *protocol.Series) error {
		pointCount := len(series.Points)
		if pointCount <= 1 {
			if nextPoint != nil {
				series.Points = append(series.Points, nextPoint)
			}
			response := &protocol.Response{Type: &endStreamResponse, Series: series, RequestId: request.Id}

			self.WriteResponse(conn, response)
			return nil
		}
		oldNextPoint := nextPoint
		nextPoint = series.Points[pointCount-1]
		series.Points[pointCount-1] = nil
		if oldNextPoint != nil {
			copy(series.Points[1:], series.Points[0:])
			series.Points[0] = oldNextPoint
		} else {
			series.Points = series.Points[:len(series.Points)-1]
		}

		response := &protocol.Response{Series: series, Type: &queryResponse, RequestId: request.Id}
		if nextPoint != nil {
			response.NextPointTime = nextPoint.Timestamp
		}
		err := self.WriteResponse(conn, response)
		return err
	}
	// the query should always parse correctly since it was parsed at the originating server.
	query, _ := parser.ParseQuery(*request.Query)
	user := self.clusterConfig.GetDbUser(*request.Database, *request.UserName)

	var ringFilter func(database, series *string, time *int64) bool
	if request.RingLocationsToQuery != nil {
		ringFilter = self.clusterConfig.GetRingFilterFunction(*request.Database, *request.RingLocationsToQuery)
	}
	self.db.ExecuteQuery(user, *request.Database, query, assignNextPointTimesAndSend, ringFilter)
}

func (self *ProtobufRequestHandler) WriteResponse(conn net.Conn, response *protocol.Response) error {
	data, err := response.Encode()
	if err != nil {
		log.Println("ProtobufRequestHandler error encoding response: ", err)
		return err
	}
	err = binary.Write(conn, binary.LittleEndian, uint32(len(data)))
	if err != nil {
		log.Println("ProtobufRequestHandler error writing response length: ", err)
		return err
	}
	_, err = conn.Write(data)
	return err
}
