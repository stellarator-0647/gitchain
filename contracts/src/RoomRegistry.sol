// SPDX-License-Identifier: Apache-2.0
pragma solidity ^0.8.24;

/// @title RoomRegistry
/// @notice Fixed-membership rooms. Members, the anchor writer and the creation
/// block are set once at creation; the MVP has no membership changes.
/// The registry address is also the EIP-712 verifyingContract of the protocol.
contract RoomRegistry {
    struct Room {
        address creator;
        address anchorWriter;
        uint64 createdBlock;
        uint32 memberCount;
    }

    uint256 public constant MAX_MEMBERS = 32;

    mapping(bytes32 => Room) private rooms;
    mapping(bytes32 => mapping(address => bool)) private member;
    mapping(bytes32 => address[]) private memberList;

    event RoomCreated(bytes32 indexed room, address indexed creator, address anchorWriter, address[] members);

    error ZeroRoom();
    error RoomExists();
    error BadMemberCount();
    error ZeroMember();
    error DuplicateMember();
    error CreatorNotMember();
    error AnchorWriterNotMember();

    function createRoom(bytes32 room, address[] calldata initial, address anchorWriter) external {
        if (room == bytes32(0)) revert ZeroRoom();
        if (rooms[room].createdBlock != 0) revert RoomExists();
        if (initial.length == 0 || initial.length > MAX_MEMBERS) revert BadMemberCount();
        for (uint256 i = 0; i < initial.length; i++) {
            address a = initial[i];
            if (a == address(0)) revert ZeroMember();
            if (member[room][a]) revert DuplicateMember();
            member[room][a] = true;
            memberList[room].push(a);
        }
        if (!member[room][msg.sender]) revert CreatorNotMember();
        if (!member[room][anchorWriter]) revert AnchorWriterNotMember();
        rooms[room] = Room({
            creator: msg.sender,
            anchorWriter: anchorWriter,
            createdBlock: uint64(block.number),
            memberCount: uint32(initial.length)
        });
        emit RoomCreated(room, msg.sender, anchorWriter, initial);
    }

    function isMember(bytes32 room, address who) external view returns (bool) {
        return member[room][who];
    }

    function roomInfo(bytes32 room) external view returns (Room memory) {
        return rooms[room];
    }

    function membersOf(bytes32 room) external view returns (address[] memory) {
        return memberList[room];
    }
}
