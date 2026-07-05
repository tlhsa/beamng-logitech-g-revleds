-- This Source Code Form is subject to the terms of the bCDDL, v. 1.1.
--
-- Logitech G rev-LED telemetry protocol for BeamNG.drive
-- ------------------------------------------------------
-- BeamNG's built-in OutGauge protocol sends RPM but NOT the engine redline,
-- so it cannot scale a rev-LED bar per car. This tiny custom protocol sends
-- current RPM together with the rev-limiter RPM (and a few extras) over UDP.
--
-- It is auto-loaded by lua/vehicle/protocols.lua for any file placed in
-- lua/vehicle/protocols/ whose name is not "outgauge"/"motionSim". Those are
-- gated by the "protocols_others_enabled" setting, which defaults to TRUE,
-- so this works with no extra configuration once the mod is installed.
--
-- Companion app: the "logi-revleds" Windows helper that drives the wheel LEDs.

local M = {}

-- little-endian marker "1LEG" (0x47454C31) used by the companion app for a sanity check
local MAGIC = 0x47454C31

local cachedMaxRPM = 0
local cachedIdleRPM = 0

local function computeEngineData()
  local maxRPM, idleRPM = 0, 0
  local getByType = powertrain and powertrain.getDevicesByType
  local engines = getByType and powertrain.getDevicesByType("combustionEngine") or {}
  for _, e in ipairs(engines) do
    if e.maxRPM and e.maxRPM > maxRPM then
      maxRPM = e.maxRPM
      idleRPM = e.idleRPM or idleRPM
    end
  end
  if maxRPM == 0 then
    -- electric vehicles: use the motor's max RPM as the scale
    local motors = getByType and powertrain.getDevicesByType("electricMotor") or {}
    for _, e in ipairs(motors) do
      if e.maxRPM and e.maxRPM > maxRPM then maxRPM = e.maxRPM end
    end
  end
  return maxRPM, idleRPM
end

local function init()
  cachedMaxRPM, cachedIdleRPM = 0, 0
end

local function reset()
  -- vehicle reset / respawn / part change: re-read the engine data lazily
  cachedMaxRPM, cachedIdleRPM = 0, 0
end

local function getAddress()        return "127.0.0.1" end
local function getPort()           return 4463 end
local function getMaxUpdateRate()  return 60 end
local function isPhysicsStepUsed() return false end -- graphics step, up to 60 Hz, plenty for LEDs

local function getStructDefinition()
  -- All fields are 4 bytes -> no padding, trivial to parse on the receiver side (32 bytes total).
  return [[
    unsigned  magic;      // 0x47454C31 sanity marker
    float     rpm;        // current engine rpm (smoothed tacho value)
    float     maxRPM;     // rev-limiter / redline rpm
    float     idleRPM;    // idle rpm (may be 0)
    int       gear;       // gearIndex: reverse = -1, neutral = 0, 1..n
    unsigned  flags;      // bit0 engineRunning, bit1 shouldShift, bit2 ignitionOn
    float     throttle;   // 0..1
    unsigned  vehicleId;  // object id
  ]]
end

local function fillStruct(o, dtSim)
  if not electrics.values.rpm then
    -- vehicle not fully initialised yet, skip sending this frame
    return
  end

  if cachedMaxRPM == 0 then
    cachedMaxRPM, cachedIdleRPM = computeEngineData()
  end

  local flags = 0
  if (electrics.values.engineRunning or 0) ~= 0 then flags = flags + 1 end
  if electrics.values.shouldShift                then flags = flags + 2 end
  if (electrics.values.ignitionLevel or 0) >= 2  then flags = flags + 4 end

  o.magic     = MAGIC
  o.rpm       = electrics.values.rpmTacho or electrics.values.rpm or 0
  o.maxRPM    = cachedMaxRPM
  o.idleRPM   = cachedIdleRPM
  o.gear      = electrics.values.gearIndex or 0
  o.flags     = flags
  o.throttle  = electrics.values.throttle or 0
  o.vehicleId = objectId or 0
end

M.init = init
M.reset = reset
M.getAddress = getAddress
M.getPort = getPort
M.getMaxUpdateRate = getMaxUpdateRate
M.getStructDefinition = getStructDefinition
M.fillStruct = fillStruct
M.isPhysicsStepUsed = isPhysicsStepUsed

return M
