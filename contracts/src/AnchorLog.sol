// SPDX-License-Identifier: Apache-2.0
pragma solidity ^0.8.24;

import {RoomRegistry} from "./RoomRegistry.sol";

/// @title AnchorLog
/// @notice Checkpoints of a room's signed event set, written only by the
/// room's anchor writer and chained by prevIndex. The contract cannot see Git:
/// ancestry, Merkle correctness and body availability are checked off chain.
contract AnchorLog {
    /// @notice prevIndex of the first checkpoint of a room.
    uint256 public constant GENESIS = type(uint256).max;

    struct Checkpoint {
        bytes32 gitCommit;
        bytes32 root;
        uint32 count;
        address by;
        uint64 blockNo;
        uint64 blockTime;
        uint256 prevIndex;
    }

    RoomRegistry public immutable registry;

    mapping(bytes32 => Checkpoint[]) private checkpoints;
    mapping(bytes32 => mapping(bytes32 => bool)) private anchoredCommit;

    event Anchored(
        bytes32 indexed room,
        uint256 indexed index,
        uint256 prevIndex,
        bytes32 gitCommit,
        bytes32 root,
        uint32 count,
        address by
    );

    error UnknownRoom();
    error NotAnchorWriter();
    error ZeroValue();
    error BadPrevIndex();
    error CountMustGrow();
    error CommitAlreadyAnchored();

    constructor(RoomRegistry registry_) {
        registry = registry_;
    }

    function anchor(bytes32 room, uint256 prevIndex, bytes32 gitCommit, bytes32 root, uint32 count)
        external
        returns (uint256 index)
    {
        RoomRegistry.Room memory r = registry.roomInfo(room);
        if (r.createdBlock == 0) revert UnknownRoom();
        if (msg.sender != r.anchorWriter) revert NotAnchorWriter();
        if (gitCommit == bytes32(0) || root == bytes32(0) || count == 0) revert ZeroValue();
        if (anchoredCommit[room][gitCommit]) revert CommitAlreadyAnchored();

        Checkpoint[] storage cps = checkpoints[room];
        index = cps.length;
        if (index == 0) {
            if (prevIndex != GENESIS) revert BadPrevIndex();
        } else {
            if (prevIndex != index - 1) revert BadPrevIndex();
            if (count <= cps[index - 1].count) revert CountMustGrow();
        }
        anchoredCommit[room][gitCommit] = true;
        cps.push(
            Checkpoint({
                gitCommit: gitCommit,
                root: root,
                count: count,
                by: msg.sender,
                blockNo: uint64(block.number),
                blockTime: uint64(block.timestamp),
                prevIndex: prevIndex
            })
        );
        emit Anchored(room, index, prevIndex, gitCommit, root, count, msg.sender);
    }

    function checkpoint(bytes32 room, uint256 index) external view returns (Checkpoint memory) {
        return checkpoints[room][index];
    }

    function checkpointCount(bytes32 room) external view returns (uint256) {
        return checkpoints[room].length;
    }
}
