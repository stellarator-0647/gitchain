// SPDX-License-Identifier: Apache-2.0
pragma solidity ^0.8.24;

import {RoomRegistry} from "./RoomRegistry.sol";

/// @title Governance
/// @notice Majority authorisation of message deletion among a room's fixed
/// members. Execution only records the authorisation on chain; nodes hide and
/// clean their own copies and a member signs the tombstone event.
contract Governance {
    struct Proposal {
        bytes32 room;
        bytes32 targetEventId;
        bytes32 reasonHash;
        address proposer;
        uint64 createdBlock;
        uint64 deadlineBlock;
        uint32 yes;
        uint32 no;
        bool executed;
    }

    RoomRegistry public immutable registry;
    uint64 public immutable votingBlocks;

    Proposal[] private proposals; // id = index + 1
    mapping(uint256 => mapping(address => bool)) public hasVoted;
    mapping(bytes32 => mapping(bytes32 => bool)) public isRedacted;

    event Proposed(uint256 indexed id, bytes32 indexed room, bytes32 targetEventId, address proposer, uint64 deadlineBlock);
    event Voted(uint256 indexed id, address indexed voter, bool support);
    event Executed(uint256 indexed id, bytes32 indexed room, bytes32 targetEventId);

    error NotMember();
    error ZeroTarget();
    error AlreadyRedacted();
    error UnknownProposal();
    error AlreadyVoted();
    error VotingClosed();
    error AlreadyExecuted();
    error NotEnoughVotes();

    constructor(RoomRegistry registry_, uint64 votingBlocks_) {
        registry = registry_;
        votingBlocks = votingBlocks_;
    }

    function proposeRedaction(bytes32 room, bytes32 targetEventId, bytes32 reasonHash) external returns (uint256 id) {
        if (!registry.isMember(room, msg.sender)) revert NotMember();
        if (targetEventId == bytes32(0)) revert ZeroTarget();
        if (isRedacted[room][targetEventId]) revert AlreadyRedacted();
        uint64 deadline = uint64(block.number) + votingBlocks;
        proposals.push(
            Proposal({
                room: room,
                targetEventId: targetEventId,
                reasonHash: reasonHash,
                proposer: msg.sender,
                createdBlock: uint64(block.number),
                deadlineBlock: deadline,
                yes: 0,
                no: 0,
                executed: false
            })
        );
        id = proposals.length;
        emit Proposed(id, room, targetEventId, msg.sender, deadline);
    }

    function vote(uint256 id, bool support) external {
        Proposal storage p = _get(id);
        if (!registry.isMember(p.room, msg.sender)) revert NotMember();
        if (p.executed) revert AlreadyExecuted();
        if (block.number > p.deadlineBlock) revert VotingClosed();
        if (hasVoted[id][msg.sender]) revert AlreadyVoted();
        hasVoted[id][msg.sender] = true;
        if (support) p.yes++;
        else p.no++;
        emit Voted(id, msg.sender, support);
    }

    /// @notice Anyone may execute; executing grants no voting power.
    function execute(uint256 id) external {
        Proposal storage p = _get(id);
        if (p.executed) revert AlreadyExecuted();
        if (block.number > p.deadlineBlock) revert VotingClosed();
        uint32 members = registry.roomInfo(p.room).memberCount;
        if (uint256(p.yes) * 2 <= members) revert NotEnoughVotes();
        if (isRedacted[p.room][p.targetEventId]) revert AlreadyRedacted();
        p.executed = true;
        isRedacted[p.room][p.targetEventId] = true;
        emit Executed(id, p.room, p.targetEventId);
    }

    function proposal(uint256 id) external view returns (Proposal memory) {
        return _get(id);
    }

    function proposalCount() external view returns (uint256) {
        return proposals.length;
    }

    function _get(uint256 id) private view returns (Proposal storage) {
        if (id == 0 || id > proposals.length) revert UnknownProposal();
        return proposals[id - 1];
    }
}
