zone z "Zone" {
  room hub "The Hub" {
    exit north -> north_room
    exit northeast -> northeast_room
    exit east -> east_room
    exit southeast -> southeast_room
    exit south -> south_room
    exit southwest -> southwest_room
    exit west -> west_room
    exit northwest -> northwest_room
    exit up -> up_room
    exit down -> down_room
    exit in -> in_room
    exit out -> out_room
  }

  room north_room "Room north" {
    exit south -> hub
  }

  room northeast_room "Room northeast" {
    exit southwest -> hub
  }

  room east_room "Room east" {
    exit west -> hub
  }

  room southeast_room "Room southeast" {
    exit northwest -> hub
  }

  room south_room "Room south" {
    exit north -> hub
  }

  room southwest_room "Room southwest" {
    exit northeast -> hub
  }

  room west_room "Room west" {
    exit east -> hub
  }

  room northwest_room "Room northwest" {
    exit southeast -> hub
  }

  room up_room "Room up" {
    exit down -> hub
  }

  room down_room "Room down" {
    exit up -> hub
  }

  room in_room "Room in" {
    exit out -> hub
  }

  room out_room "Room out" {
    exit in -> hub
  }
}
