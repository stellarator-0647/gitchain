// SPDX-License-Identifier: Apache-2.0
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {RoomRegistry} from "../src/RoomRegistry.sol";
import {AnchorLog} from "../src/AnchorLog.sol";
import {Governance} from "../src/Governance.sol";

contract GitChainTest is Test {
    RoomRegistry reg;
    AnchorLog log_;
    Governance gov;

    address alice = makeAddr("alice");
    address bob = makeAddr("bob");
    address carol = makeAddr("carol");
    address mallory = makeAddr("mallory");
    bytes32 constant ROOM = keccak256("room");
    bytes32 constant TARGET = keccak256("event");

    function setUp() public {
        reg = new RoomRegistry();
        log_ = new AnchorLog(reg);
        gov = new Governance(reg, 100);
        vm.prank(alice);
        reg.createRoom(ROOM, _members(), alice);
    }

    function _members() internal view returns (address[] memory m) {
        m = new address[](3);
        m[0] = alice;
        m[1] = bob;
        m[2] = carol;
    }

    // ---- RoomRegistry ----

    function test_RoomInfo() public view {
        RoomRegistry.Room memory r = reg.roomInfo(ROOM);
        assertEq(r.creator, alice);
        assertEq(r.anchorWriter, alice);
        assertEq(r.memberCount, 3);
        assertTrue(reg.isMember(ROOM, bob));
        assertFalse(reg.isMember(ROOM, mallory));
        assertEq(reg.membersOf(ROOM).length, 3);
    }

    function test_RevertWhen_RoomExists() public {
        vm.prank(alice);
        vm.expectRevert(RoomRegistry.RoomExists.selector);
        reg.createRoom(ROOM, _members(), alice);
    }

    function test_RevertWhen_CreatorNotMember() public {
        vm.prank(mallory);
        vm.expectRevert(RoomRegistry.CreatorNotMember.selector);
        reg.createRoom(keccak256("r2"), _members(), alice);
    }

    function test_RevertWhen_AnchorWriterNotMember() public {
        vm.prank(alice);
        vm.expectRevert(RoomRegistry.AnchorWriterNotMember.selector);
        reg.createRoom(keccak256("r2"), _members(), mallory);
    }

    function test_RevertWhen_DuplicateMember() public {
        address[] memory m = new address[](2);
        m[0] = alice;
        m[1] = alice;
        vm.prank(alice);
        vm.expectRevert(RoomRegistry.DuplicateMember.selector);
        reg.createRoom(keccak256("r2"), m, alice);
    }

    function test_RevertWhen_ZeroMember() public {
        address[] memory m = new address[](2);
        m[0] = alice;
        m[1] = address(0);
        vm.prank(alice);
        vm.expectRevert(RoomRegistry.ZeroMember.selector);
        reg.createRoom(keccak256("r2"), m, alice);
    }

    // ---- AnchorLog ----

    function _anchor(uint256 prev, bytes32 commit, uint32 count) internal returns (uint256) {
        vm.prank(alice);
        return log_.anchor(ROOM, prev, commit, keccak256(abi.encode(commit)), count);
    }

    function test_AnchorChain() public {
        uint256 genesis = log_.GENESIS();
        assertEq(_anchor(genesis, bytes32(uint256(1)), 3), 0);
        assertEq(_anchor(0, bytes32(uint256(2)), 5), 1);
        assertEq(log_.checkpointCount(ROOM), 2);
        AnchorLog.Checkpoint memory c = log_.checkpoint(ROOM, 1);
        assertEq(c.count, 5);
        assertEq(c.prevIndex, 0);
        assertEq(c.by, alice);
    }

    function test_RevertWhen_NonWriterAnchors() public {
        uint256 genesis = log_.GENESIS();
        vm.prank(bob);
        vm.expectRevert(AnchorLog.NotAnchorWriter.selector);
        log_.anchor(ROOM, genesis, bytes32(uint256(1)), bytes32(uint256(2)), 1);
    }

    function test_RevertWhen_FirstPrevNotGenesis() public {
        vm.prank(alice);
        vm.expectRevert(AnchorLog.BadPrevIndex.selector);
        log_.anchor(ROOM, 0, bytes32(uint256(1)), bytes32(uint256(2)), 1);
    }

    function test_RevertWhen_WrongPrevIndex() public {
        _anchor(log_.GENESIS(), bytes32(uint256(1)), 3);
        _anchor(0, bytes32(uint256(2)), 4);
        vm.prank(alice);
        vm.expectRevert(AnchorLog.BadPrevIndex.selector);
        log_.anchor(ROOM, 0, bytes32(uint256(3)), bytes32(uint256(9)), 5);
    }

    function test_RevertWhen_CountDoesNotGrow() public {
        _anchor(log_.GENESIS(), bytes32(uint256(1)), 3);
        vm.prank(alice);
        vm.expectRevert(AnchorLog.CountMustGrow.selector);
        log_.anchor(ROOM, 0, bytes32(uint256(2)), bytes32(uint256(9)), 3);
    }

    function test_RevertWhen_ZeroValues() public {
        uint256 genesis = log_.GENESIS();
        vm.startPrank(alice);
        vm.expectRevert(AnchorLog.ZeroValue.selector);
        log_.anchor(ROOM, genesis, bytes32(0), bytes32(uint256(2)), 1);
        vm.expectRevert(AnchorLog.ZeroValue.selector);
        log_.anchor(ROOM, genesis, bytes32(uint256(1)), bytes32(0), 1);
        vm.expectRevert(AnchorLog.ZeroValue.selector);
        log_.anchor(ROOM, genesis, bytes32(uint256(1)), bytes32(uint256(2)), 0);
        vm.stopPrank();
    }

    function test_RevertWhen_CommitReused() public {
        _anchor(log_.GENESIS(), bytes32(uint256(1)), 3);
        vm.prank(alice);
        vm.expectRevert(AnchorLog.CommitAlreadyAnchored.selector);
        log_.anchor(ROOM, 0, bytes32(uint256(1)), bytes32(uint256(9)), 4);
    }

    function test_RevertWhen_UnknownRoom() public {
        uint256 genesis = log_.GENESIS();
        vm.prank(alice);
        vm.expectRevert(AnchorLog.UnknownRoom.selector);
        log_.anchor(keccak256("nope"), genesis, bytes32(uint256(1)), bytes32(uint256(2)), 1);
    }

    // ---- Governance ----

    function _propose() internal returns (uint256 id) {
        vm.prank(alice);
        id = gov.proposeRedaction(ROOM, TARGET, keccak256("reason"));
    }

    function test_TwoOfThreeDeletes() public {
        uint256 id = _propose();
        vm.prank(alice);
        gov.vote(id, true);
        vm.prank(bob);
        gov.vote(id, true);
        vm.prank(mallory); // anyone may execute
        gov.execute(id);
        assertTrue(gov.isRedacted(ROOM, TARGET));
        assertTrue(gov.proposal(id).executed);
    }

    function test_RevertWhen_NotEnoughVotes() public {
        uint256 id = _propose();
        vm.prank(alice);
        gov.vote(id, true);
        vm.prank(bob);
        gov.vote(id, false);
        vm.expectRevert(Governance.NotEnoughVotes.selector);
        gov.execute(id);
    }

    function test_RevertWhen_NonMemberProposes() public {
        vm.prank(mallory);
        vm.expectRevert(Governance.NotMember.selector);
        gov.proposeRedaction(ROOM, TARGET, bytes32(0));
    }

    function test_RevertWhen_NonMemberVotes() public {
        uint256 id = _propose();
        vm.prank(mallory);
        vm.expectRevert(Governance.NotMember.selector);
        gov.vote(id, true);
    }

    function test_RevertWhen_DoubleVote() public {
        uint256 id = _propose();
        vm.startPrank(bob);
        gov.vote(id, true);
        vm.expectRevert(Governance.AlreadyVoted.selector);
        gov.vote(id, false);
        vm.stopPrank();
    }

    function test_RevertWhen_VoteAfterDeadline() public {
        uint256 id = _propose();
        vm.roll(block.number + 101);
        vm.prank(bob);
        vm.expectRevert(Governance.VotingClosed.selector);
        gov.vote(id, true);
    }

    function test_RevertWhen_ExecuteAfterDeadline() public {
        uint256 id = _propose();
        vm.prank(alice);
        gov.vote(id, true);
        vm.prank(bob);
        gov.vote(id, true);
        vm.roll(block.number + 101);
        vm.expectRevert(Governance.VotingClosed.selector);
        gov.execute(id);
    }

    function test_RevertWhen_ExecuteTwice() public {
        uint256 id = _propose();
        vm.prank(alice);
        gov.vote(id, true);
        vm.prank(bob);
        gov.vote(id, true);
        gov.execute(id);
        vm.expectRevert(Governance.AlreadyExecuted.selector);
        gov.execute(id);
    }

    function test_RevertWhen_TargetAlreadyRedacted() public {
        test_TwoOfThreeDeletes();
        vm.prank(bob);
        vm.expectRevert(Governance.AlreadyRedacted.selector);
        gov.proposeRedaction(ROOM, TARGET, bytes32(0));
    }

    function test_RevertWhen_ZeroTarget() public {
        vm.prank(alice);
        vm.expectRevert(Governance.ZeroTarget.selector);
        gov.proposeRedaction(ROOM, bytes32(0), bytes32(0));
    }

    function test_RevertWhen_UnknownProposal() public {
        vm.expectRevert(Governance.UnknownProposal.selector);
        gov.proposal(7);
    }
}
