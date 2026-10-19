// SPDX-License-Identifier: Apache-2.0
pragma solidity ^0.8.24;

import {Script, console} from "forge-std/Script.sol";
import {RoomRegistry} from "../src/RoomRegistry.sol";
import {AnchorLog} from "../src/AnchorLog.sol";
import {Governance} from "../src/Governance.sol";

/// Deploys the three contracts. VOTING_BLOCKS defaults to 100.
/// forge script script/Deploy.s.sol --rpc-url $RPC --broadcast --private-key $KEY
contract Deploy is Script {
    function run() external {
        uint64 votingBlocks = uint64(vm.envOr("VOTING_BLOCKS", uint256(100)));
        vm.startBroadcast();
        RoomRegistry reg = new RoomRegistry();
        AnchorLog anchorLog = new AnchorLog(reg);
        Governance gov = new Governance(reg, votingBlocks);
        vm.stopBroadcast();
        console.log("registry", address(reg));
        console.log("anchorLog", address(anchorLog));
        console.log("governance", address(gov));
    }
}
