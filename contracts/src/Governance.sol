// SPDX-License-Identifier: Apache-2.0
pragma solidity ^0.8.24;

import {RoomRegistry} from "./RoomRegistry.sol";

/// @title Governance
/// @notice Majority authorisation of message deletion among a room's fixed
/// members. Execution only records the authorisation on chain; nodes hide and
/// clean their own copies and a member signs the tombstone event.
/// @notice Also settles the order of two concurrent events (neither author had
/// seen the other's) by majority vote. The contract cannot see Git, so it only
/// records the agreement; nodes apply it to concurrent pairs and ignore it
/// where the signed causal order already decides.
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

    /// @dev Outcome: 0 open, 1 `a` before `b`, 2 `b` before `a`.
    struct OrderProposal {
        bytes32 room;
        bytes32 a;
        bytes32 b;
        address proposer;
        uint64 createdBlock;
        uint64 deadlineBlock;
        uint32 forA;
        uint32 forB;
        uint8 outcome;
    }

    RoomRegistry public immutable registry;
    uint64 public immutable votingBlocks;

    Proposal[] private proposals; // id = index + 1
    mapping(uint256 => mapping(address => bool)) public hasVoted;
    mapping(bytes32 => mapping(bytes32 => bool)) public isRedacted;

    OrderProposal[] private orderProposals; // id = index + 1
    mapping(uint256 => mapping(address => bool)) public hasVotedOrder;
    /// @dev room => keccak256(min(a,b), max(a,b)) => latest order proposal id.
    mapping(bytes32 => mapping(bytes32 => uint256)) public orderOfPair;

    event Proposed(uint256 indexed id, bytes32 indexed room, bytes32 targetEventId, address proposer, uint64 deadlineBlock);
    event Voted(uint256 indexed id, address indexed voter, bool support);
    event Executed(uint256 indexed id, bytes32 indexed room, bytes32 targetEventId);
    event OrderProposed(uint256 indexed id, bytes32 indexed room, bytes32 a, bytes32 b, address proposer, uint64 deadlineBlock);
    event OrderVoted(uint256 indexed id, address indexed voter, bool aFirst);
    event OrderSettled(uint256 indexed id, bytes32 indexed room, bytes32 before, bytes32 later);

    error NotMember();
    error ZeroTarget();
    error AlreadyRedacted();
    error UnknownProposal();
    error AlreadyVoted();
    error VotingClosed();
    error AlreadyExecuted();
    error NotEnoughVotes();
    error SameEvent();
    error OrderOpen();
    error AlreadyOrdered();
    error UnknownOrder();

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

    /// @notice Asks the members to settle whether `a` comes before `b`. One
    /// proposal per pair at a time; a pair can be retried only after an earlier
    /// proposal expired without a majority.
    function proposeOrder(bytes32 room, bytes32 a, bytes32 b) external returns (uint256 id) {
        if (!registry.isMember(room, msg.sender)) revert NotMember();
        if (a == bytes32(0) || b == bytes32(0)) revert ZeroTarget();
        if (a == b) revert SameEvent();
        bytes32 key = pairKey(a, b);
        uint256 prev = orderOfPair[room][key];
        if (prev != 0) {
            OrderProposal storage q = orderProposals[prev - 1];
            if (q.outcome != 0) revert AlreadyOrdered();
            if (block.number <= q.deadlineBlock) revert OrderOpen();
        }
        uint64 deadline = uint64(block.number) + votingBlocks;
        orderProposals.push(
            OrderProposal({
                room: room,
                a: a,
                b: b,
                proposer: msg.sender,
                createdBlock: uint64(block.number),
                deadlineBlock: deadline,
                forA: 0,
                forB: 0,
                outcome: 0
            })
        );
        id = orderProposals.length;
        orderOfPair[room][key] = id;
        emit OrderProposed(id, room, a, b, msg.sender, deadline);
    }

    /// @notice Votes for `a` first (true) or `b` first (false). The vote that
    /// gives one side a majority settles the order immediately.
    function voteOrder(uint256 id, bool aFirst) external {
        OrderProposal storage p = _getOrder(id);
        if (!registry.isMember(p.room, msg.sender)) revert NotMember();
        if (p.outcome != 0) revert AlreadyOrdered();
        if (block.number > p.deadlineBlock) revert VotingClosed();
        if (hasVotedOrder[id][msg.sender]) revert AlreadyVoted();
        hasVotedOrder[id][msg.sender] = true;
        uint32 count = aFirst ? ++p.forA : ++p.forB;
        emit OrderVoted(id, msg.sender, aFirst);
        if (uint256(count) * 2 > registry.roomInfo(p.room).memberCount) {
            p.outcome = aFirst ? 1 : 2;
            (bytes32 first, bytes32 second) = aFirst ? (p.a, p.b) : (p.b, p.a);
            emit OrderSettled(id, p.room, first, second);
        }
    }

    function orderProposal(uint256 id) external view returns (OrderProposal memory) {
        return _getOrder(id);
    }

    function orderProposalCount() external view returns (uint256) {
        return orderProposals.length;
    }

    function pairKey(bytes32 a, bytes32 b) public pure returns (bytes32) {
        return a < b ? keccak256(abi.encodePacked(a, b)) : keccak256(abi.encodePacked(b, a));
    }

    function proposal(uint256 id) external view returns (Proposal memory) {
        return _get(id);
    }

    function proposalCount() external view returns (uint256) {
        return proposals.length;
    }

    function _getOrder(uint256 id) private view returns (OrderProposal storage) {
        if (id == 0 || id > orderProposals.length) revert UnknownOrder();
        return orderProposals[id - 1];
    }

    function _get(uint256 id) private view returns (Proposal storage) {
        if (id == 0 || id > proposals.length) revert UnknownProposal();
        return proposals[id - 1];
    }
}
